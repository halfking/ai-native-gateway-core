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
			if isNonRoutableAddressFragment(fragment) {
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
// a loopback / private / link-local / unspecified target.
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
	case TypeServerIP, TypeInternalIP, SensitiveType("server_ipv6"):
	default:
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(fragment.Value))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
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
