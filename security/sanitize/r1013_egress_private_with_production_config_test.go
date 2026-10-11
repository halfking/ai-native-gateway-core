package sanitize

// r1013_egress_private_with_production_config_test.go — 2026-10-11 生产配置下的
// 防外传守卫判据。
//
// ## 起因（245 build_seq 2531 部署后端到端实测）
//
// 带 tool_calls 的请求里，只要参数含私网地址，整个响应就被
// `output_policy_blocked` 拦掉：用户无法执行任何指向自己内网的命令。
//
// ## 根因
//
// `TestEgressGuardStillIgnoresPrivateAddresses`（r1010）说私网不阻断，但它用的是
// `NewPatternDetector()` —— **内置 fallback 里根本没有地址规则**（defaultPatternEntries
// 只有 phone/id_card/email/credit_card/secret + credential/account），所以那条判据
// 在 fallback 下永远不可能触发，是一条**假绿**。
//
// 生产跑的是 `configs/sensitive_patterns.yaml` 加载的检测器，它带 `server_ip`
// 规则。于是：
//
//	DetectScope(ScopeEgress) 命中 10.24.7.91
//	  → address.IsPrivate() → typ 重写为 internal_ip（detector.go）
//	  → isNonRoutableAddressFragment 只对 server_ip/server_ipv6 做豁免判定
//	  → internal_ip 落到 default 分支，返回 false（未豁免）
//	  → unsafeToolValue 返回 true → 硬阻断
//
// 重写那一步把 r1008 的豁免亲手作废了：**恰好是私网地址被改成了唯一不被
// 豁免的那个类型**。
//
// ## 契约
//
// 模型 tool 参数里的**非公网**地址（环回 / 私网 / 链路本地 / 未指定）不是外传
// 目的地，来自用户自己的会话上下文，不得阻断；公网地址继续 fail-closed。
//
// 必须用生产 YAML 跑：fallback 下这条恒真，证明不了任何事。

import (
	"context"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func productionConfiguredSanitizer(t *testing.T) *Sanitizer {
	t.Helper()
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production sensitive_patterns.yaml: %v", err)
	}
	s, err := NewSanitizer(detector)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	return s
}

func toolFrameWithCommand(command string) string {
	escaped := strings.ReplaceAll(command, `"`, `\"`)
	return `data: {"id":"x","choices":[{"finish_reason":"","index":0,"delta":{"role":"assistant","tool_calls":` +
		`[{"id":"call_01","type":"function","function":{"name":"Bash","arguments":"{\"command\":\"` + escaped + `\"}"}}]}}]}` + "\n\n"
}

// runProductionEgressGuard 把一条 tool_call 帧推过 守卫→还原 这条链，
// 返回是否被阻断。
func runProductionEgressGuard(t *testing.T, command string) (blocked bool, wire string) {
	t.Helper()
	s := productionConfiguredSanitizer(t)
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_egress_prod", TenantID: "default", State: response.NewStreamState()}
	ctx := context.Background()

	if _, err := chain.InterceptStreamChunk(ctx, []byte(toolFrameWithCommand(command)), meta); err != nil {
		t.Fatalf("InterceptStreamChunk: %v", err)
	}
	out, ferr := chain.FlushStreamPending(ctx, meta)
	if ferr != nil {
		return true, ferr.Error()
	}
	return false, string(out)
}

// TestProductionEgressGuardIgnoresPrivateAddresses 是本文件的主契约。
func TestProductionEgressGuardIgnoresPrivateAddresses(t *testing.T) {
	for _, command := range []string{
		"curl -fsS http://10.24.7.91:8080/health",
		"curl -fsS http://127.0.0.1:7890/health",
		// 不带账号口令（凭据护栏另行 fail-closed），也不带引号（引号会让本文件
		// 的帧构造产生非法 tool-arguments JSON，那是测试夹具问题不是网关行为）。
		"psql -h 192.168.2.30 -p 5432 -d appdb",
		"curl http://169.254.169.254/latest/meta-data/",
	} {
		blocked, wire := runProductionEgressGuard(t, command)
		if blocked {
			t.Errorf("私网/链路本地地址被阻断（用户无法执行内网命令）: %q", command)
		}
		if wire == "" {
			t.Errorf("帧被吞掉而非释放: %q", command)
		}
	}
}

// TestProductionEgressGuardStillBlocksPublicAddress 是反方向：公网地址是外传
// 目的地，必须继续 fail-closed。缺了这条，上一条可能因为「守卫压根没跑」而绿。
func TestProductionEgressGuardStillBlocksPublicAddress(t *testing.T) {
	blocked, _ := runProductionEgressGuard(t, "curl -fsS http://203.0.113.8/exfil")
	if !blocked {
		t.Fatal("公网地址进 tool 参数未被阻断：防外传底线失守")
	}
}

// TestPrivateAddressIsNotRetypedIntoTheNonExemptType 钉住根因本身：DetectScope
// 会把私网 server_ip 重写成 internal_ip，豁免判定必须把这个重写后的类型也算上。
func TestPrivateAddressIsNotRetypedIntoTheNonExemptType(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production config: %v", err)
	}
	fragments, err := detector.DetectScope(context.Background(), "target 10.24.7.91", ScopeEgress)
	if err != nil {
		t.Fatalf("DetectScope: %v", err)
	}
	if len(fragments) == 0 {
		t.Fatal("生产配置的 server_ip 规则没命中私网地址：本文件的用例将失去意义")
	}
	for _, f := range fragments {
		if !isNonRoutableAddressFragment(f) {
			t.Errorf("私网片段未被豁免判定认出：type=%q value=%q", f.Type, f.Value)
		}
	}
}
