// bg/pricing_baseline_alias_test.go — 观察价查找**不做**近似匹配的承重判据
//
// 这条与同文件里的抓取/摊平测试刻意分开：那边测的是「出网那一步」，
// 这边测的是「键到底允许多不允许多」。两者混在一起，读代码的人会以为
// 近似匹配是抓取行为，而不是查找策略。
//
// ★ 为什么值得单独钉：2026-10-06 实测 models.dev（216 providers）对
// models_canonical 960 条的**原厂键覆盖率是 87 条 = 9.1%**（9 家有定价页
// 快照的原厂范围内是 87/454 = 19.2%）。这个数字看起来像个待修的缺口 ——
// 「只差一个 `-latest` 后缀」「只差一个 `open-` 前缀」就能多捞回 7 条。
//
// ★ 逐条查价之后，那 7 条**一条都不能合**：
//
//	我们的名            源里的名                          合了会怎样
//	codestral          codestral-latest                 -latest 是**会移动的别名**
//	ministral-8b       ministral-8b-latest               基准价挂在别名上 = 价随
//	gpt-5.2-chat       gpt-5.2-chat-latest                版本漂移，且漂移无声
//	mistral-large      mistral-large-latest/-2411/-2512  源里有**三个**日期版本，
//	                                                       我们的目录只有一行 ⇒
//	                                                       合了就等于随便挑一个
//	mixtral-8x22b      open-mixtral-8x22b                open- 是**开源权重版**，
//	                                                       和托管版不是同一个产品
//
// ⇒ 「覆盖率低」在这里**不是缺陷**，是正确行为；严格键匹配是**承重**的。
//
//	任何为了把 9.1% 做高而放松 LookupObservation 的改动，都会把价挂到会移动
//	的别名、挑错的版本、或另一个产品线上去。
//
// 本判据钉的就是这件事：上面 4 个裸名必须**查不到**，而它们的精确名必须
// **查得到**（阳性对照 —— 否则这条可能只是因为表是空的而恒真）。
package bg

import "testing"

func TestLookupObservationRefusesNearMissAliases(t *testing.T) {
	p := func(f float64) *float64 { return &f }
	observed := observedPrices{
		"mistral": {
			// 会移动的别名：下一个版本一发布，这个键就指向别的东西了。
			"codestral-latest":    {InputPer1M: p(0.3), OutputPer1M: p(0.9)},
			"ministral-8b-latest": {InputPer1M: p(0.1), OutputPer1M: p(0.1)},
			"gpt-5.2-chat-latest": {InputPer1M: p(1.75), OutputPer1M: p(14)},
			// 家族名：源里三个日期版本，三个价都不一样。
			"mistral-large-latest": {InputPer1M: p(0.5), OutputPer1M: p(1.5)},
			"mistral-large-2411":   {InputPer1M: p(1.0), OutputPer1M: p(3.0)},
			"mistral-large-2512":   {InputPer1M: p(0.5), OutputPer1M: p(1.5)},
			// 另一个产品线：开源权重版。
			"open-mixtral-8x22b": {InputPer1M: p(2.0), OutputPer1M: p(6.0)},
		},
	}

	// 阳性对照：精确名必须查得到，且价必须对。先钉住这个，再钉下面那些
	// 「查不到」—— 否则「全部查不到」和「表是空的」在读数上完全一样。
	for _, tc := range []struct {
		model string
		in    float64
	}{
		{"codestral-latest", 0.3},
		{"mistral-large-2411", 1.0},
		{"mistral-large-2512", 0.5},
		{"open-mixtral-8x22b", 2.0},
	} {
		got, ok := observed.LookupObservation("mistral", tc.model)
		if !ok {
			t.Fatalf("量具自证失败：精确名 %q 都查不到，下面那些「查不到」就证明不了任何东西",
				tc.model)
		}
		if got.InputPer1M == nil || *got.InputPer1M != tc.in {
			t.Errorf("%q input = %v want %v", tc.model, got.InputPer1M, tc.in)
		}
	}

	// 承重部分：这些裸名必须查不到。
	for _, tc := range []struct{ bare, nearMiss, why string }{
		{"codestral", "codestral-latest",
			"-latest is a moving alias: a baseline price on it drifts silently when the vendor ships the next version"},
		{"ministral-8b", "ministral-8b-latest",
			"same moving-alias problem"},
		{"mistral-large", "mistral-large-2411 / -2512 / -latest",
			"the catalog has one row for a family; the source has three dated versions — merging picks one at random"},
		{"mixtral-8x22b", "open-mixtral-8x22b",
			"open- is the open-weight release, a different product from the hosted one"},
	} {
		if got, ok := observed.LookupObservation("mistral", tc.bare); ok {
			t.Errorf("LookupObservation(mistral, %q) matched %q at input=%v — %s. "+
				"Resolving a baseline price through a near-miss name puts the price on a moving "+
				"alias, on the wrong version, or on a different product line.",
				tc.bare, tc.nearMiss, derefF(got.InputPer1M), tc.why)
		}
	}
}

func derefF(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
