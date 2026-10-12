package sanitize

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
)

// naiveFoldCaseWord is the 2026-10-11 12h 审计 A1 之前的朴素实现。
//
// 保留它不是留历史包袱，而是当差分判据的**基准侧**：A1 把过滤器从朴素
// 循环换成首字节闸 + memchr 之后，两者在语义上必须逐位等价。这个等价性
// 没有别的判据能保证——A1 的修复过程中就真的先写错过一版（"SeNsItIvE"
// 因只取先命中的那条 memchr 而被判成不存在），是靠本文件抓出来的。
//
// 所以：本文件判的是**语义**；成本由 BenchmarkMayCarryMarkerPrefilter 判。
// 两者缺一，修复都可能悄悄退回去。
func naiveFoldCaseWord(text, word string) bool {
	n := len(word)
	for i := 0; i+n <= len(text); i++ {
		match := true
		for j := 0; j < n; j++ {
			c := text[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != word[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestFoldCaseWordMatchesNaiveSemantics 钉住 containsFoldCaseWord 与
// containsFoldCaseWordBytes 相对朴素实现**逐位等价**。
//
// 两个实现族都在跑的三组语料：
//  1. 随机差分——字母表刻意富含 s/S/{/} 与 \x00，最大化大小写与边界组合；
//  2. 真实拼写的全部子串——覆盖首字节命中但整体不匹配、以及匹配恰好落在
//     末尾的两种边界；
//  3. 单字节与空串——防越界（实现期真出过 index out of range）。
func TestFoldCaseWordMatchesNaiveSemantics(t *testing.T) {
	word := placeholderMarkerWord

	// (1) 随机差分。
	alphabet := []byte("sSeEnSiTiIvVeE:{}\x00 abcXYZ.-_0129")
	rng := rand.New(rand.NewSource(20261011))
	for iter := 0; iter < 200000; iter++ {
		b := make([]byte, rng.Intn(40))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		s := string(b)
		want := naiveFoldCaseWord(s, word)
		if got := containsFoldCaseWord(s, word); got != want {
			t.Fatalf("containsFoldCaseWord(%q) = %v, want %v", s, got, want)
		}
		if got := containsFoldCaseWordBytes(b, word); got != want {
			t.Fatalf("containsFoldCaseWordBytes(%q) = %v, want %v", s, got, want)
		}
	}

	// (2) 真实拼写的全部子串。mixed-case 那条是 A1 实测漏判的样本。
	for _, w := range []string{
		"SENSITIVE:phone:1",
		"sensitive:x:2",
		"{SENSITIVE:a:1}",
		"SeNsItIvE:z:9",
		"｛SENSITIVE:q:1｝",
	} {
		for i := 0; i <= len(w); i++ {
			for j := i; j <= len(w); j++ {
				sub := w[i:j]
				want := naiveFoldCaseWord(sub, word)
				if got := containsFoldCaseWord(sub, word); got != want {
					t.Fatalf("substr %q: got %v want %v", sub, got, want)
				}
			}
		}
	}

	// (3) 单字节与空串：防越界。
	for i := 0; i < 256; i++ {
		s := string([]byte{byte(i)})
		if got, want := containsFoldCaseWord(s, word), naiveFoldCaseWord(s, word); got != want {
			t.Fatalf("single byte %d: got %v want %v", i, got, want)
		}
	}
	if got, want := containsFoldCaseWord("", word), naiveFoldCaseWord("", word); got != want {
		t.Fatalf("empty: got %v want %v", got, want)
	}
}

// TestMayCarryMarkerStillAdmitsBareForm 钉住 A1 修复**没有把原始修复目标
// 一起修掉**：裸形（SENSITIVE:phone:1，无花括号）必须继续让过滤器放行。
//
// A1 是性能修正，不是语义修正——这条是为了让「为了快而漏判裸形」不可行。
func TestMayCarryMarkerStillAdmitsBareForm(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{"bare upper", "the SENSITIVE:phone:1 value", true},
		{"bare lower", "the sensitive:phone:1 value", true},
		{"bare mixed", "SeNsItIvE:phone:1", true},
		{"braced", "prefix {SENSITIVE:a:1} suffix", true},
		{"full width brace", "｛SENSITIVE:a:1｝", true},
		{"ordinary prose", "the quick brown fox jumps over the lazy dog", false},
		{"ordinary s words", "this is a sensitive subject in prose", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mayCarryMarker([]byte(tc.payload)); got != tc.want {
				t.Fatalf("mayCarryMarker(%q) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

// BenchmarkMayCarryMarkerPrefilter 是 A1 的**成本判据**。
//
// 参照物是修复前的两次 memchr（下面的 mayCarryMarkerBraceOnly）：
// 那条路径 0 分配、约 17-33 GB/s。A1 修掉的是一次 `string(payload)`
// 全量拷贝 + O(n·m) 朴素循环——实测在 16KB 真实响应体上是 2477ns/12288B
// 对 5.6ns/0B，约 444×。
//
// 跑法：go test ./security/sanitize/ -run XXX -bench MayCarryMarker -benchmem
// 门槛看两行：allocs/op 必须为 0，且 ns/op 与参照物同量级（不是同一个数量级差）。
// 真实 SSE 响应体含 '{'，走首条 memchr 即返回，是常态路径。
func mayCarryMarkerBraceOnly(payload []byte) bool {
	return bytes.IndexByte(payload, '{') >= 0 || bytes.IndexByte(payload, fullWidthBraceLeadByte) >= 0
}

var (
	benchSSEBody    = []byte(strings.Repeat("data: {\"delta\":\"some ordinary model output text here\"}\n\n", 200))
	benchNoBraceTxt = []byte(strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200))
	benchLargeBody  = []byte(strings.Repeat("x", 512*1024))
)

func BenchmarkMayCarryMarkerSSEBody(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarker(benchSSEBody)
	}
}

func BenchmarkMayCarryMarkerBraceOnlySSEBody(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarkerBraceOnly(benchSSEBody)
	}
}

func BenchmarkMayCarryMarkerNoBraceProse(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarker(benchNoBraceTxt)
	}
}

func BenchmarkMayCarryMarkerBraceOnlyNoBraceProse(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarkerBraceOnly(benchNoBraceTxt)
	}
}

func BenchmarkMayCarryMarkerLargeBody(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarker(benchLargeBody)
	}
}

func BenchmarkMayCarryMarkerBraceOnlyLargeBody(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = mayCarryMarkerBraceOnly(benchLargeBody)
	}
}
