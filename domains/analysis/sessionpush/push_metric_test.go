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
// 禁用=disabled；成功=success；失败=failed；空 Result=skipped。
// 每次 Deliver 调用都计 dispatched（覆盖率分母）。
//
// ★ 2026-11-03（12h 审计 P1-3）两处收口：
// ① 禁用期与空载荷**必须分档**——旧实现两者都记 skipped，且 dispatched
//   只在启用态计数 ⇒ 文档承诺的「禁用期直接可读」兑现不了。
// ② 断言一律用**增量**口径：pushDispatchTotal 是 promauto 进程级全局，
//   同包 sessionpush_test.go 也会调 Deliver（同样的 provisional/final
//   标签）⇒ 绝对值断言随用例顺序漂移、不是自包含的门。
func TestDeliverRecordsReconciliationCounters(t *testing.T) {
	read := func(status, kind string) float64 {
		return testutil.ToFloat64(pushDispatchTotal.WithLabelValues(status, kind))
	}
	// assertDeltas 断言一次 Deliver 让各档计数分别增加了多少。
	assertDeltas := func(t *testing.T, want map[[2]string]float64, fn func()) {
		t.Helper()
		before := make(map[[2]string]float64, len(want))
		for k := range want {
			before[k] = read(k[0], k[1])
		}
		fn()
		for k, w := range want {
			if got := read(k[0], k[1]) - before[k]; got != w {
				t.Errorf("%s/%s delta = %v, want %v", k[0], k[1], got, w)
			}
		}
	}

	// 禁用态：dispatched（分母）+ disabled（禁用期），不得混进 skipped。
	assertDeltas(t, map[[2]string]float64{
		{"dispatched", "final"}: 1,
		{"disabled", "final"}:   1,
		{"skipped", "final"}:    0,
	}, func() {
		Set(nil)
		Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Status: "final"})
	})

	// 成功路径：fake memora 200。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	target := fakePushTarget{url: srv.URL, project: "p1"}
	in := Input{TenantID: "t", SessionID: "s", Status: "provisional",
		Result: sessionmeta.Result{SchemaVersion: "session-analysis/v1",
			AnalysisKind: sessionmeta.AnalysisKind, Status: "provisional", InputHash: "h1",
			// ★ 必须有可沉淀内容（title/summary/outcome/key_points 至少一个非空）
			//   ⇒ 否则 BuildPayload 判 ok=false ⇒ 走 skipped 分支，推送根本
			//   不会发生（这正是 origin/main 上那条红测的根因）。
			Title: "把网关的会话分析接进记忆"}}
	assertDeltas(t, map[[2]string]float64{
		{"dispatched", "provisional"}: 1,
		{"success", "provisional"}:    1,
		{"failed", "provisional"}:     0,
	}, func() {
		Set(target)
		Deliver(context.Background(), in)
	})

	// 失败路径：500。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv2.Close()
	assertDeltas(t, map[[2]string]float64{
		{"dispatched", "provisional"}: 1,
		{"failed", "provisional"}:     1,
		{"success", "provisional"}:    0,
	}, func() {
		Set(fakePushTarget{url: srv2.URL, project: "p1"})
		Deliver(context.Background(), in)
	})

	// 空 Result ⇒ skipped（且不是 disabled）。
	assertDeltas(t, map[[2]string]float64{
		{"dispatched", "final"}: 1,
		{"skipped", "final"}:    1,
		{"disabled", "final"}:   0,
		{"success", "final"}:    0,
	}, func() {
		Set(target)
		Deliver(context.Background(), Input{TenantID: "t", SessionID: "s", Status: "final"})
	})
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
