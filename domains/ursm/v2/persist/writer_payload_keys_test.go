// 守卫：payloadDuplicateKeys 必须覆盖 writer.go 里每一处 hash["..."] 读取。
//
// 为什么需要它：818 迁移把 31 个 hash 键提升为 typed 列，却只把 7 个加进
// payloadDuplicateKeys。剩下 24 个既进了 typed 列、又被原样抄进 payload ——
// 同一个值存两份。生产实测 payload 35 键里 27 键重复，全表多占约 3436 MB。
//
// 这个缺陷不会被任何功能测试发现：功能上完全正确，只是白占空间。
// 所以只能靠「清单与代码里的实际读取保持一致」这条静态约束来守。
//
// 用法：读 writer.go 源码，抽出所有 hash["..."]，逐个要求出现在排除表里。
// 以后有人新增 typed 列却忘了加进表里，这条测试立刻红。
package persist

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

var reHashRead = regexp.MustCompile(`hash\["([a-z0-9_]+)"\]`)

func TestPayloadDuplicateKeysCoverAllTypedHashKeys(t *testing.T) {
	src, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}

	reads := map[string]bool{}
	for _, m := range reHashRead.FindAllStringSubmatch(string(src), -1) {
		reads[m[1]] = true
	}
	if len(reads) == 0 {
		t.Fatal("no hash[...] reads found in writer.go — the regex is stale, " +
			"not evidence that the exclusion list is complete")
	}

	inList := map[string]bool{}
	for _, k := range payloadDuplicateKeys {
		if inList[k] {
			t.Errorf("payloadDuplicateKeys contains %q twice", k)
		}
		inList[k] = true
	}

	var missing []string
	for k := range reads {
		if !inList[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these hash keys are read into typed columns but NOT excluded from payload, "+
			"so every one of them is stored twice:\n  %v\n"+
			"add them to payloadDuplicateKeys with a comment naming the target Row field",
			missing)
	}
}

// TestPayloadDuplicateKeysNoStaleEntries 防止反向的漂移：表里留下已经不读的键。
// 不算错（多排一个键不会丢数据 —— 该值仍在 typed 列里），但会让人误以为
// 该键仍有 typed 列，从而在真正删除 typed 列时漏改这里。
func TestPayloadDuplicateKeysNoStaleEntries(t *testing.T) {
	src, err := os.ReadFile("writer.go")
	if err != nil {
		t.Fatalf("read writer.go: %v", err)
	}
	reads := map[string]bool{}
	for _, m := range reHashRead.FindAllStringSubmatch(string(src), -1) {
		reads[m[1]] = true
	}

	var stale []string
	for _, k := range payloadDuplicateKeys {
		if !reads[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("payloadDuplicateKeys lists keys writer.go no longer reads: %v\n"+
			"remove them, or confirm the typed column was intentionally kept", stale)
	}
}
