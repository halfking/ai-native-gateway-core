// bg/modality_challenge_test.go — 语义判别挑战的生成与判读
//
// 这组测试里最要紧的是 TestChallengeImageReallyContainsItsAnswer：
// 判据自带的陷阱是「声称的正确答案」与「实际画进图里的颜色」可以不一致
// ——比如渲染循环把 x0 算错、或分隔带宽度让偏移错位。那时判读器会
// 拿着一张与标注不符的图去判模型，整套核实的结论系统性错误，而
// 所有只测 GradeVisionAnswer 的用例照样全绿。
//
// 所以这里从**编码后的 PNG 反解像素**去核对，不复用渲染期的任何中间
// 状态。
package bg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	mathrand "math/rand/v2"
	"strings"
	"testing"
)

// decodeChallengePNG 把挑战的 data URL 反解回位图。
func decodeChallengePNG(t *testing.T, dataURL string) image.Image {
	t.Helper()
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("data URL prefix = %q, want %q", dataURL[:min(32, len(dataURL))], prefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

// paletteRGBByName 反查色板里的 RGB，供像素核对用。
func paletteRGBByName(t *testing.T, name string) (uint8, uint8, uint8) {
	t.Helper()
	for _, c := range challengePalette {
		if c.name == name {
			return c.rgb.R, c.rgb.G, c.rgb.B
		}
	}
	t.Fatalf("color %q not in palette", name)
	return 0, 0, 0
}

func TestChallengeImageReallyContainsItsAnswer(t *testing.T) {
	rng := mathrand.New(mathrand.NewPCG(7, 11))

	for round := 0; round < 200; round++ {
		ch, err := NewVisionChallenge(rng)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(ch.Colors) != visionChallengeBlocks {
			t.Fatalf("round %d: len(Colors)=%d want %d", round, len(ch.Colors), visionChallengeBlocks)
		}

		// 色块必须互不相同——重复色块会让「顺序」退化，盲猜概率上升。
		seen := map[string]bool{}
		for _, c := range ch.Colors {
			if seen[c] {
				t.Fatalf("round %d: duplicate color %q in %v", round, c, ch.Colors)
			}
			seen[c] = true
		}

		img := decodeChallengePNG(t, ch.DataURL)
		bounds := img.Bounds()
		wantW := len(ch.Colors)*challengeBlockPx + (len(ch.Colors)-1)*challengeSeparatorPx
		if bounds.Dx() != wantW || bounds.Dy() != challengeBlockPx {
			t.Fatalf("round %d: image bounds %dx%d want %dx%d",
				round, bounds.Dx(), bounds.Dy(), wantW, challengeBlockPx)
		}

		// 逐块取中心像素核对。取中心而不是角上：角上可能被编码器或
		// 未来的抗锯齿改动影响，中心是最稳的采样点。
		for i, wantName := range ch.Colors {
			x := i*(challengeBlockPx+challengeSeparatorPx) + challengeBlockPx/2
			y := challengeBlockPx / 2
			got := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			wr, wg, wb := paletteRGBByName(t, wantName)
			if got.R != wr || got.G != wg || got.B != wb || got.A != 0xFF {
				t.Fatalf("round %d block %d: pixel(%d,%d)=(%d,%d,%d,%d) want %q=(%d,%d,%d,255)",
					round, i, x, y, got.R, got.G, got.B, got.A, wantName, wr, wg, wb)
			}
		}

		// 分隔带必须真的是灰的（且灰不在色板内），否则相邻块会糊成一片。
		for i := 0; i < len(ch.Colors)-1; i++ {
			x := (i+1)*challengeBlockPx + i*challengeSeparatorPx + challengeSeparatorPx/2
			got := color.RGBAModel.Convert(img.At(x, challengeBlockPx/2)).(color.RGBA)
			if got.R != challengeGray.R || got.G != challengeGray.G || got.B != challengeGray.B {
				t.Fatalf("round %d separator %d: pixel=(%d,%d,%d) want gray (%d,%d,%d)",
					round, i, got.R, got.G, got.B, challengeGray.R, challengeGray.G, challengeGray.B)
			}
		}
	}
}

func TestChallengeIsFreshEachRound(t *testing.T) {
	rng := mathrand.New(mathrand.NewPCG(1, 2))
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		ch, err := NewVisionChallenge(rng)
		if err != nil {
			t.Fatal(err)
		}
		seen[ch.DataURL] = true
	}
	// 50 次全部不同说明挑战在动。若这条红了，模型只需记住上一张图
	// 就能过关，判据失效。
	if len(seen) != 50 {
		t.Fatalf("distinct challenges = %d want 50", len(seen))
	}
}

func TestGradeVisionAnswer(t *testing.T) {
	ch := VisionChallenge{Colors: []string{"red", "green", "blue", "yellow"}}

	cases := []struct {
		name        string
		answer      string
		wantMatched bool
		wantScore   int
		wantMention []string
	}{
		{
			name:        "canonical english csv",
			answer:      "red, green, blue, yellow",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			name:        "no separators",
			answer:      "redgreenblueyellow",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			name:        "chinese with prose",
			answer:      "从左到右依次是：红、绿、蓝、黄。",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			name:        "chinese with 的 particles",
			answer:      "第一个是红色的，第二个是绿色的，第三个是蓝色的，第四个是黄色的",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			// 同义词必须归一到**这张挑战图**的颜色上：crimson→red、
			// lime→green。答案里出现 orange（orange 确实在色板里）会
			// 判负，因为第 4 块是 yellow。
			name:        "synonyms",
			answer:      "crimson, lime, blue, yellow",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			// 钉住「歧义词不猜」这条纪律：gold 既被用来叫 #FFFF00(黄)
			// 也被用来叫 #FFA500(橙)，所以它不在同义词表里。模型这么答
			// 时判读落进 inconclusive（颜色词只有 3 个 < 4），由调用方记
			// unknown —— 既不升级也不降级。若哪天把 gold 收进表里，这条
			// 用例会立刻红。
			name:        "ambiguous gold is not counted",
			answer:      "crimson, emerald, blue, gold",
			wantMatched: false, wantScore: 3,
			wantMention: []string{"red", "green", "blue"},
		},
		{
			name:        "uppercase",
			answer:      "RED, GREEN, BLUE, YELLOW",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
		{
			// 顺序错就是错：顺序本身是判据的一部分。score 是**位置**命中
			// 数，这个错位排列恰好在第 2、4 位对上了 ⇒ score=2 而
			// matched=false。「颜色全对但顺序全错」正是判读失败时最需要
			// 一眼看出的形态，所以位置分与定论必须分开看。
			name:        "right colors wrong order",
			answer:      "blue, green, red, yellow",
			wantMatched: false, wantScore: 2,
			wantMention: []string{"blue", "green", "red", "yellow"},
		},
		{
			name:        "one color wrong",
			answer:      "red, green, black, yellow",
			wantMatched: false, wantScore: 3,
			wantMention: []string{"red", "green", "black", "yellow"},
		},
		{
			// 盲模型的典型自由发挥：说了些颜色，但没覆盖全 4 个。
			name:        "partial guess",
			answer:      "looks like a blue and yellow image",
			wantMatched: false, wantScore: 0,
			wantMention: []string{"blue", "yellow"},
		},
		{
			// 关键用例：颜色词不足 4 个 ⇒ inconclusive，调用方必须记
			// unknown 而不是 negative。空回答是最容易误判成 negative 的
			// 形态（推理模型把 token 花光、正文被截断）。
			name:        "empty answer is inconclusive not negative",
			answer:      "",
			wantMatched: false, wantScore: 0,
			wantMention: nil,
		},
		{
			name:        "refusal has no colors",
			answer:      "I cannot process images.",
			wantMatched: false, wantScore: 0,
			wantMention: nil,
		},
		{
			// 灰色不在色板内，说它不加分也不减分。
			name:        "gray separator ignored",
			answer:      "gray, red, gray, green, gray, blue, gray, yellow",
			wantMatched: true, wantScore: 4,
			wantMention: []string{"red", "green", "blue", "yellow"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, score, mention := GradeVisionAnswer(tc.answer, ch)
			if matched != tc.wantMatched {
				t.Errorf("matched = %v want %v (answer=%q mention=%v)", matched, tc.wantMatched, tc.answer, mention)
			}
			if score != tc.wantScore {
				t.Errorf("score = %d want %d (answer=%q mention=%v)", score, tc.wantScore, tc.answer, mention)
			}
			if len(mention) != len(tc.wantMention) {
				t.Fatalf("mention = %v want %v", mention, tc.wantMention)
			}
			for i := range mention {
				if mention[i] != tc.wantMention[i] {
					t.Fatalf("mention = %v want %v", mention, tc.wantMention)
				}
			}
		})
	}
}

// 判读器必须能把「没有结论」和「有结论但错」区分开：前者是
// len(mention) < 4，调用方据此记 unknown；后者是 mention>=4 但没匹配上。
// 这条把那条纪律钉在判读器的输出形状上，而不是只写在注释里。
func TestGradeVisionAnswerSeparatesInconclusiveFromWrong(t *testing.T) {
	ch := VisionChallenge{Colors: []string{"red", "green", "blue", "yellow"}}

	_, _, inconclusive := GradeVisionAnswer("I can only see text", ch)
	if len(inconclusive) >= visionChallengeBlocks {
		t.Fatalf("inconclusive case produced %d color mentions, want <%d", len(inconclusive), visionChallengeBlocks)
	}

	_, _, wrong := GradeVisionAnswer("purple, orange, black, white", ch)
	if len(wrong) < visionChallengeBlocks {
		t.Fatalf("wrong-answer case produced %d color mentions, want >=%d", len(wrong), visionChallengeBlocks)
	}
}
