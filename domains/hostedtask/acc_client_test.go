package hostedtask

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dispatch 契约（§2.1/§3.1 ②）：POST /api/v2/runtime/dispatch + Idempotency-Key
// + Bearer；202 {command_id}；缺 command_id 的 2xx 必须报错（防假派发）。
func TestACCClientDispatch(t *testing.T) {
	var gotKey, gotAuth, gotTaskID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/runtime/dispatch" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotKey = r.Header.Get("Idempotency-Key")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"task_id":"hosted-ht_1"`) {
			gotTaskID = "hosted-ht_1"
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"cmd-1","run_id":"run-9"}`))
	}))
	defer srv.Close()

	c := NewACCClient(srv.URL, "tok-1", "rt-1", srv.Client())
	res, err := c.Dispatch(context.Background(), DispatchRequest{
		RuntimeID: "rt-1", TaskID: "hosted-ht_1", Payload: DispatchPayload{Kind: "pi", Prompt: "p"},
	}, "gw-hosted-ht_1-a1")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if gotKey != "gw-hosted-ht_1-a1" || gotAuth != "Bearer tok-1" {
		t.Errorf("headers wrong: key=%q auth=%q", gotKey, gotAuth)
	}
	if gotTaskID != "hosted-ht_1" {
		t.Errorf("task_id body missing")
	}
	if res.CommandID != "cmd-1" || res.RunID != "run-9" {
		t.Errorf("res = %+v", res)
	}

	// 202 无 command_id → 错误。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer bad.Close()
	if _, err := NewACCClient(bad.URL, "t", "r", bad.Client()).Dispatch(context.Background(),
		DispatchRequest{TaskID: "x"}, "k"); err == nil {
		t.Error("2xx without command_id must error")
	}
}

// cancel 契约（§3.2/§0-F4）：2xx = requested（不承诺 effective）。
func TestACCClientCancelAndPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			if r.Method != http.MethodPost {
				t.Errorf("cancel method = %s", r.Method)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"requested"}`))
		case strings.Contains(r.URL.Path, "/api/v2/runtime/commands/"):
			_, _ = w.Write([]byte(`{"data":{"command_id":"cmd-1","status":"COMPLETED","result":{"stop_reason":"stop_success","output":"done"}}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewACCClient(srv.URL, "t", "r", srv.Client())

	if err := c.Cancel(context.Background(), "cmd-1"); err != nil {
		t.Errorf("cancel: %v", err)
	}
	cmd, err := c.GetCommand(context.Background(), "cmd-1")
	if err != nil {
		t.Fatalf("getCommand: %v", err)
	}
	if !cmd.Done() || cmd.StopReason != "stop_success" || cmd.StopIsError() {
		t.Errorf("cmd parse wrong: %+v", cmd)
	}
}

func TestACCClientNotConfigured(t *testing.T) {
	c := NewACCClient("", "", "", nil)
	if c.Configured() {
		t.Error("empty base/token must be unconfigured")
	}
	if err := c.Cancel(context.Background(), "x"); err == nil {
		t.Error("unconfigured cancel must error (degraded path relies on it)")
	}
}
