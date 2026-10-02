package admin

// auto_route_tuning_r64_test.go — R64 修复的单测（纯函数 + 无 DB handler）。
//
// 覆盖：
//   - handleAnalyze 对 bg.ErrAnalyzeInProgress 哨兵回 409「分析进行中」，
//     其他错误仍走 writeInternalErr 的 500（R64 P2）；
//   - thresholds.llm_confidence 只降不升闸门的纯函数判定（R64 P3）；
//   - errProposalConflict 包装错误能被 approveProposal 的 errors.Is 分支
//     识别（409 回显映射的契约前提）。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// stubAnalyzer returns a canned error from AnalyzeOnce.
type r64StubAnalyzer struct{ err error }

func (s *r64StubAnalyzer) AnalyzeOnce(ctx context.Context) error { return s.err }

func TestHandleAnalyzeBusyReturns409(t *testing.T) {
	h := NewTuningHandlers(&AutoRouteHandlers{})
	h.SetAnalyzer(&r64StubAnalyzer{err: bg.ErrAnalyzeInProgress})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/auto-route/tuning/analyze", nil)
	rec := httptest.NewRecorder()
	h.handleAnalyze(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "分析进行中") {
		t.Fatalf("body = %s, want message containing 分析进行中", rec.Body.String())
	}
}

func TestHandleAnalyzeInternalErrorStill500(t *testing.T) {
	h := NewTuningHandlers(&AutoRouteHandlers{})
	h.SetAnalyzer(&r64StubAnalyzer{err: errors.New("boom")})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/auto-route/tuning/analyze", nil)
	rec := httptest.NewRecorder()
	h.handleAnalyze(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for non-sentinel errors", rec.Code)
	}
}

func TestThresholdOnlyLowerViolation(t *testing.T) {
	// R64 P3：只降不升——升档/持平/current 不可解析（fail-closed）都算
	// 违规，真正的降档放行。
	cases := []struct {
		name      string
		newRaw    string
		current   string
		violation bool
	}{
		{"lower ok", "0.60", "0.70", false},
		{"equal is violation", "0.70", "0.70", true},
		{"raise is violation", "0.85", "0.70", true},
		{"unparseable current fails closed", "0.60", "abc", true},
		{"whitespace tolerated", " 0.60 ", " 0.70 ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, bad := thresholdOnlyLowerViolation(c.newRaw, c.current)
			if bad != c.violation {
				t.Fatalf("violation = %v (msg %q), want %v", bad, msg, c.violation)
			}
			if bad && msg == "" {
				t.Fatal("violation must carry a reason message")
			}
			if !bad && msg != "" {
				t.Fatalf("allowed apply must not carry a violation message, got %q", msg)
			}
		})
	}
}

func TestErrProposalConflictSurvivesWrapping(t *testing.T) {
	// approveProposal 用 errors.Is(err, errProposalConflict) 决定 409 回显，
	// conflictErrf 的包装必须保持可识别且保留明细文本。
	err := conflictErrf("thresholds.llm_confidence %v must be lower than current %v (only-lower guard)", 0.8, 0.7)
	if !errors.Is(err, errProposalConflict) {
		t.Fatal("conflictErrf error must match errProposalConflict via errors.Is")
	}
	if !strings.Contains(err.Error(), "only-lower guard") {
		t.Fatalf("error text = %q, want detail preserved", err.Error())
	}
}
