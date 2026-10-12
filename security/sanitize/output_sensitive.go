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
		return &outputcompliance.ComplianceResult{Blocked: true, RedactedOutput: "[REDACTED]", Issues: []outputcompliance.ComplianceIssue{{Type: "sensitive", Subtype: "credential", Content: "[REDACTED]", Severity: 8}}}, nil
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
		fragments, err := c.sanitizer.detector.Detect(ctx, value)
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
// a loopback / private / link-local target. See unsafeToolValue's string case
// for the 2026-10-08 incident rationale.
func isNonRoutableAddressFragment(fragment SensitiveFragment) bool {
	switch fragment.Type {
	case TypeServerIP, SensitiveType("server_ipv6"):
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
		safe.WriteString("[REDACTED]")
		last = span.end
		result.Issues = append(result.Issues, outputcompliance.ComplianceIssue{
			Type: "sensitive", Subtype: string(span.typ), Content: "[REDACTED]", Severity: 8, Score: 1, Redacted: c.action != OutputBlock,
		})
	}
	safe.WriteString(text[last:])
	result.RedactedOutput = safe.String()
	result.Blocked = c.action == OutputBlock
	return result, nil
}
