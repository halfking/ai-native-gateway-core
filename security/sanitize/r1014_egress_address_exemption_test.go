package sanitize

// r1014_egress_address_exemption_test.go — 2026-10-11 批判性审计。
//
// 承接 dc63f19ff（私网被硬阻断）。那次把 TypeInternalIP 补进豁免类型集合，
// 修的是**症状**；根因是豁免判定写成了「枚举可放行的谓词」——
//
//	ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
//
// 这个方向把「没列举到的」默认成外传，于是每出现一个新网段就复发一次同类故障。
// 审计实测复发：100.64.0.0/10（Tailscale/WireGuard）与云元数据
// 100.100.100.100 都让指向自己内网或 mesh 的 tool 调用整条 output_policy_blocked。
//
// 改为反过来问：默认按公网可达处理（fail-closed 保住），只对**明确不可路由**
// 的地址放行。
//
// ── 判据设计上的一个教训（本文件第一版栽在这里）─────────────────────
//
// 第一版把断言写在 SSE 流式链上（构造 tool_calls 帧推过 守卫→还原 链），
// 结果它对核心逻辑的极端变异**完全无感**：
//
//	变异「豁免恒 false」= 私网全阻断   -> 0 条判据失败
//	变异「豁免恒 true」  = 公网全放行   -> 0 条判据失败
//
// 原因：流式链上帧在 hold/release 的边缘语义与守卫内部的判定不是同一件事，
// 我测的是链的行为，而变异改的是守卫的判定，两者之间没有必然联系。
//
// 第二次栽在更隐蔽的地方：把 arguments 写成 JSON 对象而不是 JSON 字符串字面量，
// 整帧不是合法 JSON，守卫压根走不到地址检查——**公网用例反而全绿**。
//
// 所以这里改成直接驱动守卫自己的判定入口（unsafeToolValue），并保留一条
// 端到端用例覆盖流式链路形态。判据必须与被测函数在同一层，否则测的是别的东西。
//
// ── 判据设计上的第二个教训（「看不见」型假绿）─────────────────────────
//
// 「没被阻断」有两种成因：(a) 命中豁免放行；(b) 检测器压根没匹配到该地址。
// 上面那两种假绿都是改逻辑，变异验证能抓到；(b) 不是改逻辑，变异验证**测不到**。
// 实测：把 server_ip 正则收窄成只匹配 127.* 之后，本文件的允许侧判据依然全绿，
// 而 TestR1014AllowedAddressesAreActuallyDetected 红 7 条。
// 所以凡有允许侧判据，必须配一条「前提仍在场」的断言。
import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func productionYAMLDetector(t *testing.T) *PatternDetector {
	t.Helper()
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("production pattern config: %v", err)
	}
	return d
}

// egressGuardBlocks 直接问守卫「这个 tool 参数是否被判为外传风险」。
// 这是守卫在生产里做判定的那个入口，判据与被测逻辑同层。
func egressGuardBlocks(t *testing.T, d *PatternDetector, toolArguments string) bool {
	t.Helper()
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
	// 生产里 tool arguments 是一个 JSON 文档，先解成结构再问守卫。
	var value any
	if err := json.Unmarshal([]byte(toolArguments), &value); err != nil {
		t.Fatalf("tool arguments must be valid JSON: %v (%s)", err, toolArguments)
	}
	// 空 toolName = 命令类调用（默认 fail-closed），与 r1014 语义一致；
	// r1020 的惰性工具豁免另有判据文件覆盖。
	blocked, err := checker.unsafeToolValue(context.Background(), value, "", 0, "")
	if err != nil {
		t.Fatalf("unsafeToolValue: %v", err)
	}
	return blocked
}

// TestR1014NonExfiltrationAddressesAreNotBlocked 锁「不阻断」这一侧。
func TestR1014NonExfiltrationAddressesAreNotBlocked(t *testing.T) {
	d := productionYAMLDetector(t)
	for _, tc := range []struct{ why, command string }{
		{"loopback", `{"command":"curl -fsS http://127.0.0.1:8080/health"}`},
		{"rfc1918 10/8", `{"command":"ssh root@10.24.7.91 'systemctl status nginx'"}`},
		{"rfc1918 192.168/16", `{"command":"ssh root@192.168.1.1 'cat /proc/uptime'"}`},
		{"rfc1918 172.16/12", `{"command":"psql -h 172.16.31.10 -U app -d app"}`},
		// 本次修复新增：CGNAT 100.64/10，net.IsPrivate() 不覆盖，
		// 但 Tailscale / WireGuard 节点都在这里。
		{"cgnat tailscale", `{"command":"tailscale ssh root@100.64.1.1 'ls /opt'"}`},
		{"cgnat upper bound", `{"command":"ping -c1 100.127.255.254"}`},
		// 云元数据走 IsLinkLocalUnicast；这条锁住它是显式意图而非侥幸命中。
		{"cloud metadata", `{"command":"curl http://169.254.169.254/latest/meta-data/iam/"}`},
		{"link-local ipv6", `{"command":"curl -g http://[fe80::1]/status"}`},
		{"multicast", `{"command":"ping -c1 224.0.0.251"}`},
	} {
		t.Run(tc.why, func(t *testing.T) {
			if egressGuardBlocks(t, d, tc.command) {
				t.Errorf("指向非外传地址的 tool 调用被判为外传风险: %s", tc.command)
			}
		})
	}
}

// TestR1014AllowedAddressesAreActuallyDetected 锁上一条判据的**前提**。
//
// 上一条只断言「没被阻断」，而「没被阻断」有两种完全不同的成因：
//
//	(a) 地址被检测到，命中豁免判定，于是放行  <- 这是本方案想要的
//	(b) 生产检测器压根没匹配到这个地址        <- 判据假绿
//
// 只锁 (a) 不锁 (b) 的话，将来谁把 server_ip / server_ipv6 的正则收窄一点，
// 允许侧会集体静默变绿，看起来「豁免在工作」，其实守卫已经看不见这些地址了。
// 变异的眼睛是看不见「看不见」的，所以必须单独判它。
func TestR1014AllowedAddressesAreActuallyDetected(t *testing.T) {
	d := productionYAMLDetector(t)
	exemptable := map[SensitiveType]bool{
		TypeServerIP:   true,
		TypeInternalIP: true,
		typeServerIPv6: true,
	}
	for _, tc := range []struct{ addr, inText string }{
		{"127.0.0.1", "curl http://127.0.0.1:8080/health"},
		{"10.24.7.91", "ssh root@10.24.7.91 up"},
		{"192.168.1.1", "ssh root@192.168.1.1 up"},
		{"172.16.31.10", "psql -h 172.16.31.10 -d app"},
		{"100.64.1.1", "tailscale ssh root@100.64.1.1 up"},
		{"100.127.255.254", "ping -c1 100.127.255.254"},
		{"100.100.100.100", "curl http://100.100.100.100/latest/meta-data/"},
		{"169.254.169.254", "curl http://169.254.169.254/latest/meta-data/iam/"},
		{"224.0.0.251", "ping -c1 224.0.0.251"},
		{"fe80::1", "curl -g http://[fe80::1]/status"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			frags, err := d.DetectScope(context.Background(), tc.inText, ScopeEgress)
			if err != nil {
				t.Fatalf("DetectScope: %v", err)
			}
			var got []SensitiveFragment
			for _, f := range frags {
				if f.Value == tc.addr {
					got = append(got, f)
				}
			}
			if len(got) == 0 {
				t.Fatalf("生产检测器在 ScopeEgress 下没检测到 %s —— 上一条判据的"+
					"「不阻断」是假绿（成因是看不见，不是豁免）", tc.addr)
			}
			for _, f := range got {
				if !exemptable[f.Type] {
					t.Errorf("%s 被检出为类型 %q，不在豁免类型集合里；"+
						"守卫会硬阻断它（这正是 dc63f19ff 修的那类故障）", tc.addr, f.Type)
				}
			}
		})
	}
}

// TestR1014PublicAddressesStillBlocked 锁「仍阻断」这一侧（反方向）。
//
// 不是凑数：缺了它，上一条可能因为「守卫压根没匹配到任何东西」而全绿。
// 172.32.0.1 专门锁 172.16/12 的边界，收窄笔误会把它错放行。
func TestR1014PublicAddressesStillBlocked(t *testing.T) {
	d := productionYAMLDetector(t)
	for _, tc := range []struct{ why, command string }{
		{"public ipv4", `{"command":"curl http://203.0.113.8/exfil"}`},
		{"public dns", `{"command":"curl http://8.8.8.8/"}`},
		{"just outside rfc1918", `{"command":"curl http://172.32.0.1/exfil"}`},
		{"just below cgnat", `{"command":"curl http://100.63.255.255/exfil"}`},
		{"just above cgnat", `{"command":"curl http://100.128.0.1/exfil"}`},
		{"public ipv6", `{"command":"curl -g http://[2001:db8::1]/exfil"}`},
	} {
		t.Run(tc.why, func(t *testing.T) {
			if !egressGuardBlocks(t, d, tc.command) {
				t.Errorf("公网地址未被判为外传风险，防外传底线破了: %s", tc.command)
			}
		})
	}
}

// TestR1014ExemptionPredicateDirection 锁根因本身：豁免判定是「先按公网处理，
// 再对明确不可路由者放行」，而不是「列举可放行的谓词」。
//
// 用 CGNAT 边界与组播做探针——如果实现退回枚举写法，新增网段就得改代码；
// 方向对了，新网段自然落在正确的一侧。
func TestR1014ExemptionPredicateDirection(t *testing.T) {
	for _, tc := range []struct {
		addr  string
		allow bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.0.1", true},
		{"169.254.169.254", true},
		{"100.64.0.0", true},      // CGNAT 下界
		{"100.127.255.255", true}, // CGNAT 上界
		{"100.100.100.100", true}, // 云元数据（阿里云）
		{"0.0.0.0", true},
		{"224.0.0.251", true},
		{"::1", true},
		{"fe80::1", true},
		{"172.32.0.1", false},     // 172.16/12 之外 = 公网
		{"100.63.255.255", false}, // CGNAT 之外
		{"100.128.0.1", false},    // CGNAT 之外
		{"8.8.8.8", false},
		{"203.0.113.8", false},
		{"2001:db8::1", false},
		{"not-an-ip", false}, // 解析不出 → 不豁免（fail-closed）
	} {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isNonExfiltrationAddress(tc.addr); got != tc.allow {
				t.Errorf("isNonExfiltrationAddress(%s) = %v, want %v", tc.addr, got, tc.allow)
			}
		})
	}
}

// TestR1014StreamLaneEndToEnd 保留一条流式链路的端到端用例，覆盖守卫在实际
// SSE 帧上的行为。刻意只断言私网不阻断——公网侧由上面两条同层判据负责，
// 混在流式链路里断言只会重复第一版踩过的坑。
func TestR1014StreamLaneEndToEnd(t *testing.T) {
	d := productionYAMLDetector(t)
	s, err := NewSanitizer(d)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}
	guard := NewOutputSensitiveInterceptor(s, OutputMask)
	restore, err := NewSanitizeRestoreInterceptor(s, nil, 0)
	if err != nil {
		t.Fatalf("NewSanitizeRestoreInterceptor: %v", err)
	}
	chain := response.NewInterceptorChain(guard, restore)
	meta := &response.StreamMeta{SessionID: "gw_r1014_stream", TenantID: "default", State: response.NewStreamState()}

	// arguments 必须是 JSON 字符串字面量（线上真实形状），payload 自身的引号
	// 要再转义一层，否则整帧非法 JSON、守卫走不到地址检查。
	arguments, err := json.Marshal(map[string]string{"command": "tailscale ssh root@100.64.1.1 'ls /opt'"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	escaped, err := json.Marshal(string(arguments))
	if err != nil {
		t.Fatalf("escape: %v", err)
	}
	frame := `data: {"id":"x","choices":[{"finish_reason":"","index":0,"delta":{"role":"assistant",` +
		`"tool_calls":[{"id":"call_01","type":"function","function":{"name":"Bash",` +
		`"arguments":` + string(escaped) + `}}]}}]}` + "\n\n"
	if _, err := chain.InterceptStreamChunk(context.Background(), []byte(frame), meta); err != nil {
		t.Fatalf("hold error: %v", err)
	}
	if _, ferr := chain.FlushStreamPending(context.Background(), meta); ferr != nil {
		t.Fatalf("CGNAT tool call was blocked end to end: %v", ferr)
	}
}
