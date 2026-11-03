package sessionpush

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	memclient "github.com/kaixuan/llm-gateway-go/domains/memory/client"
)

// TestDeliverRecordsReconciliationCounters 钉住 P1-2① 对账口径：
// 禁用=skipped；成功=dispatched+success；失败=dispatched+failed；
// 空 Result=skipped。
func TestDeliverRecordsReconciliationCounters(t *testing.T) {
	// 禁用态。
	Set(nil)
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Status: "final"})
	if got := testutil.ToFloat64(pushDispatchTotal.WithLabelValues("skipped", "final")); got != 1 {
		t.Fatalf("disabled push skipped/final = %v, want 1", got)
	}

	// 成功路径：fake memora 200。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	target := fakePushTarget{url: srv.URL, project: "p1"}
	Set(target)
	// Result 需含可落内容（HasLandableContent：Title/Summary/Outcome/KeyPoints
	// 至少其一）——空壳 Result 会被 BuildPayload 判 skipped。
	in := Input{TenantID: "t", SessionID: "s", Status: "provisional",
		Result: sessionmeta.Result{SchemaVersion: "session-analysis/v1",
			AnalysisKind: sessionmeta.AnalysisKind, Status: "provisional",
			InputHash: "h1", Title: "会话标题"}}
	Deliver(context.Background(), in)
	if got := testutil.ToFloat64(pushDispatchTotal.WithLabelValues("dispatched", "provisional")); got != 1 {
		t.Fatalf("dispatched/provisional = %v, want 1", got)
	}
	if got := testutil.ToFloat64(pushDispatchTotal.WithLabelValues("success", "provisional")); got != 1 {
		t.Fatalf("success/provisional = %v, want 1", got)
	}

	// 失败路径：500。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	Set(fakePushTarget{url: srv2.URL, project: "p1"})
	Deliver(context.Background(), in)
	if got := testutil.ToFloat64(pushDispatchTotal.WithLabelValues("failed", "provisional")); got != 1 {
		t.Fatalf("failed/provisional = %v, want 1", got)
	}

	// 空 Result ⇒ skipped。
	Set(target)
	Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Status: "final"})
	if got := testutil.ToFloat64(pushDispatchTotal.WithLabelValues("skipped", "final")); got != 2 {
		t.Fatalf("empty-result skipped/final = %v, want 2", got)
	}
}

// fakePushTarget 实现 PushTarget（绕开 memclient 的 env 构造链）。
type fakePushTarget struct {
	url     string
	project string
}

func (f fakePushTarget) Disabled() bool    { return false }
func (f fakePushTarget) BaseURL() string   { return f.url }
func (f fakePushTarget) ProjectID() string { return f.project }

func (f fakePushTarget) Push(ctx context.Context, payload memclient.SessionAnalysisPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatusErr(resp.StatusCode)
	}
	return nil
}

type httpStatusErr int

func (e httpStatusErr) Error() string { return "http error status" }
