package main

import (
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/vendorprice"
)

func ptr(f float64) *float64 { return &f }

// TestApplyUnitGate 是**消费侧**那道独立复核的判据。
//
// 它和提取器里的那道重复，是故意的。两道判据的失效方式不同：提取器改版、
// 页面改版、或有人放宽提取器口径时，提取器那道会先失效，而这一道正好是
// 把候选带进 SSOT 形状的最后一关。
//
// 变异实测：把 applyUnitGate 直接返回入参，上面这条立刻转红——
// 也就是说这道门确实在咬，而不是一个恒真的摆设。
func TestApplyUnitGate(t *testing.T) {
	t.Run("rejects a non-token unit", func(t *testing.T) {
		in := ptr(0.002)
		c := applyUnitGate(vendorprice.Candidate{
			Model: "some-image-model", Input: in,
			Unit: vendorprice.UnitPerImage,
			// 故意把置信度设成可用：提取器一旦放宽口径，这就是它会给出的东西。
			Confidence: vendorprice.ConfidenceTableRow,
		})
		if c.Confidence == vendorprice.ConfidenceTableRow {
			t.Fatal("a per-image price was allowed through to the review queue — " +
				"0.002 per image in a per-1M column is four orders of magnitude off")
		}
		if !strings.Contains(strings.Join(c.Warnings, " "), "proposal tool") {
			t.Errorf("rejection must name itself so a reviewer knows which gate fired: %v", c.Warnings)
		}
	})

	for _, unit := range []string{
		vendorprice.UnitPerSecond,
		vendorprice.UnitPerHour,
		vendorprice.UnitPer1KCalls,
		vendorprice.UnitPerChar,
		vendorprice.UnitPerMinute,
		vendorprice.UnitPerMessage,
	} {
		t.Run("rejects "+unit, func(t *testing.T) {
			c := applyUnitGate(vendorprice.Candidate{
				Unit: unit, Confidence: vendorprice.ConfidenceTableRow,
			})
			if c.Confidence == vendorprice.ConfidenceTableRow {
				t.Errorf("unit %q passed the gate", unit)
			}
		})
	}

	t.Run("passes per_1m through untouched", func(t *testing.T) {
		in := ptr(5.0)
		c := applyUnitGate(vendorprice.Candidate{
			Model: "some-text-model", Input: in,
			Unit: vendorprice.UnitPer1M, Confidence: vendorprice.ConfidenceTableRow,
		})
		if c.Confidence != vendorprice.ConfidenceTableRow {
			t.Errorf("a genuine per-1M price was rejected: %v", c.Warnings)
		}
		if c.Input != in {
			t.Error("the gate must not touch the price value")
		}
	})

	t.Run("passes an unstated unit through", func(t *testing.T) {
		// 单位未知时提取器自己就判了 unusable；这一道不该在这里替它猜。
		c := applyUnitGate(vendorprice.Candidate{
			Unit: vendorprice.UnitUnknown, Confidence: vendorprice.ConfidenceUnusable,
		})
		if len(c.Warnings) != 0 {
			t.Errorf("gate added a warning for an unstated unit: %v", c.Warnings)
		}
	})
}

// 端到端：实抓的 xAI 页面里那些按图/按秒计费的价格，一条都不许进
// ready_to_review。
func TestApplyUnitGate_EndToEndOnLiveXaiPage(t *testing.T) {
	raw, err := os.ReadFile("../../internal/vendorprice/testdata/live-xai-pricing.md")
	if err != nil {
		t.Skipf("live fixture unavailable: %v", err)
	}
	var leaked []string
	for _, c := range vendorprice.Extract("xai", "https://docs.x.ai/developers/models", raw) {
		gated := applyUnitGate(c)
		if gated.Confidence != vendorprice.ConfidenceTableRow {
			continue
		}
		if c.Unit != "" && c.Unit != vendorprice.UnitPer1M {
			leaked = append(leaked, c.Model+" unit="+c.Unit)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("non-token units reached the review queue: %v", leaked)
	}
}
