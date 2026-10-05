package main

// 一行里点名**多个**模型时，解析层必须拒收而不是挑一个。
//
// # 缺陷本体（2026-10-06 实测）
//
// anthropic 价目页 Fast mode 那张表里有一行：
//
//	| Claude Opus 4.6 / Claude Opus 4.7 | $30 / MTok | $150 / MTok |
//
// 它的展示名原样进匹配器，得到
//
//	score=0.90  accepted=true  reason=<空>  → claude-opus-4-7
//
// —— **满分通过、零告警**，`claude-opus-4-6` 被静默丢掉。提案里这条看不出任何
// 异常，人看过去就是一个「自信的、正确的价」。
//
// 后果是 6 倍：那一行的 $30/$150 会成为 claude-opus-4-7 的基准价，而同一页
// line 162 的标准价是 $5/$25；claude-opus-4-6 仍从 line 163 拿 $5/$25 ⇒ 同一张
// 页面里的两个模型被记成两套价，而其中一套错了 6 倍。这正好打在「准确控制模型
// 实际成本」这个目标上。
//
// # 为什么这条以前没被任何门抓到
//
// 1. 抓价的那道门只看**行**：那一行格式干净、数字齐、没划线价 ⇒ 它通过。
// 2. 抓名字的那道门只看**一个展示名能否解析**：它解析成功了，分数还很高。
// 3. 真要靠本轮的撞名不变量，它也会「抓到」—— 但报出来的理由会是
//    「同一个 canonical 有两个不同的价」，而**病因根本不是价冲突**，是行归错了
//    模型。一个对的诊断换成另一个错的诊断，比没诊断更费时间。
//
// # 修法：按段解析，不查词表
//
// 把单元格按 " / " 切开，每段各自走**原样形态**的匹配器；只有当 ≥2 段解析到
// **不同**的 canonical 时才拒收。没有词表，所以「/ 是不是分隔符」这个问题
// 不存在 —— 分隔符就是字面量，而「哪一段是哪个模型」由**名单**决定。
//
// ★ 这条今天仍是**潜伏**的：Fast mode 表在默认口径下被散文维度护栏拒收，
//   只有 `-accept-dimension-prose-as-footnote` 打开时才走到这里。判据仍然
//   直接调 `resolveCanonical`，所以它现在就在守，而不是等 flag 被批准那天。

import (
	"strings"
	"testing"
)

// realCanonicalSlice 取真实名单里确实存在的几个名字（不给判据自造名字）。
var realCanonicalSlice = []string{
	"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
	"claude-opus-4.7-fast", "claude-sonnet-4-5", "claude-sonnet-4-6",
}

func TestASlashJoinedRowNamingTwoModelsIsRefusedNotAttributedToOne(t *testing.T) {
	// ★ 用 anthropic 那一行的**原样展示名**（含空格与斜杠）。抄真实形状，
	//   不是自己编一个「A / B」。
	c := rcand("", "anthropic", "Claude Opus 4.6 / Claude Opus 4.7", px(30), px(150), 296)
	c.Confidence = "table_row"

	resolved, u := resolveCanonical(c, realCanonicalSlice)
	if resolved != nil {
		t.Fatalf("a row that names TWO models was attributed to a single canonical %q — "+
			"that silently drops the other model and records a price for a model the row "+
			"says something different about", resolvedCanonical(*resolved))
	}
	// 理由必须**点名**有哪些模型，否则人看到「拒收」还得自己回页面数一遍。
	for _, want := range []string{"claude-opus-4-6", "claude-opus-4-7", "2 models"} {
		if !strings.Contains(u.Reason, want) {
			t.Errorf("refusal reason must name %q so the reader knows which models are involved, got:\n%s",
				want, u.Reason)
		}
	}
}

// 反向钉：单模型名（哪怕带别的符号）必须照常解析。
//
// 没有这条，上面那道拒收可能变成「凡带斜杠就拒收」，而那会误伤真实页面里
// 带斜杠的单模型名。三个名字取自真实名单的形状。
func TestSingleModelNamesStillResolveNormally(t *testing.T) {
	for _, display := range []string{
		"Claude Opus 4.7",
		"Claude Opus 4.6",
		"Claude Opus 4.7 Fast",
		"Claude Sonnet 4.5",
	} {
		c := rcand("", "anthropic", display, px(5), px(25), 1)
		resolved, u := resolveCanonical(c, realCanonicalSlice)
		if resolved == nil {
			t.Errorf("%q must still resolve (reason: %s)", display, u.Reason)
		}
	}
}

// 两段都指向**同一个** canonical 时不是「一行多个模型」——
// 例如 "claude-opus-4-7 / claude-opus-4-7 (deprecated)" 这种写法。
// 判据钉住「不同」这个条件，别让它退化成「带斜杠就拒收」。
func TestSlashSegmentsResolvingToTheSameCanonicalAreNotRefused(t *testing.T) {
	c := rcand("", "anthropic", "Claude Opus 4.7 / claude-opus-4-7", px(5), px(25), 1)
	resolved, u := resolveCanonical(c, realCanonicalSlice)
	if resolved == nil {
		t.Errorf("two segments that resolve to the SAME canonical are not a multi-model row; "+
			"refusing them would block a legitimate price (reason: %s)", u.Reason)
	}
}

// 只有一段能解析 ⇒ 不是多模型行（另一段是散文或注释）。
// 真实语料里 doubao 的 "speech-2.6-turbo / speech-02-turbo" 就是这种形状。
func TestSlashRowWithOnlyOneResolvableSegmentIsNotAMultiModelRow(t *testing.T) {
	c := rcand("", "doubao", "Claude Opus 4.7 / some prose tail", px(1), px(2), 64)
	if names := multiModelCanonicals(c, realCanonicalSlice); len(names) > 1 {
		t.Errorf("only one segment resolves, so this is not a row naming several models; got %v", names)
	}
}
