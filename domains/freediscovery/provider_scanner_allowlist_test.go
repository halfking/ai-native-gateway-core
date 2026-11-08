package freediscovery

// provider_scanner_allowlist_test.go — 2026-11-09（运营审计轮项5）。
// 钉：扫描器私网白名单只来自显式 env（默认空 = SSRF 守卫不变），
// 解析容忍空段/空白；freellmapi 内网源在白名单后可被扫描。

import (
	"net/http"
	"os"
	"testing"
)

func TestPrivateAllowlistRules(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FD_PRIVATE_ALLOWLIST", "")
	if got := PrivateAllowlistRules(); got != nil {
		t.Fatalf("unset env must keep the strict SSRF guard, got %v", got)
	}

	t.Setenv("LLM_GATEWAY_FD_PRIVATE_ALLOWLIST", " freellmapi:3001 ,, 172.21.0.0/12 ")
	got := PrivateAllowlistRules()
	if len(got) != 2 || got[0] != "freellmapi:3001" || got[1] != "172.21.0.0/12" {
		t.Fatalf("allowlist parse drifted: %v", got)
	}

	t.Setenv("LLM_GATEWAY_FD_PRIVATE_ALLOWLIST", " , , ")
	if got := PrivateAllowlistRules(); got != nil {
		t.Fatalf("empty segments only must stay nil, got %v", got)
	}
}

func TestNewHTTPScannerUsesAllowlistTransport(t *testing.T) {
	t.Setenv("LLM_GATEWAY_FD_PRIVATE_ALLOWLIST", "127.0.0.1:1")
	c := NewHTTPScanner(nil)
	if c == nil || c.doer == nil {
		t.Fatalf("scanner must carry a doer")
	}
	// 只验构造成功（allowlist 生效性由 safehttpclient 自身的门测覆盖）。
	_ = http.Client{}
	_ = os.Getenv
}
