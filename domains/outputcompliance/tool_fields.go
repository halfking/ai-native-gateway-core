package outputcompliance

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CheckField inspects decoded tool values so JSON quoting and partial scripts
// cannot turn harmless redaction code into a credential. Fragments remain held
// by the interceptor until the complete tool lane is checked.
func (c *Checker) CheckField(ctx context.Context, tenant, label, text string) (*ComplianceResult, error) {
	if label == "__tool_fragment" {
		return &ComplianceResult{Compliant: true, RedactedOutput: text}, nil
	}
	if label != "__tool_json" && !strings.HasPrefix(label, "__tool_json@") {
		return c.Check(WithRuleField(ctx, "", label), tenant, text)
	}
	if !json.Valid([]byte(text)) {
		return nil, fmt.Errorf("invalid tool argument JSON")
	}
	root, err := decodeToolJSON(text)
	if err != nil {
		return nil, err
	}
	result := &ComplianceResult{Compliant: true, RedactedOutput: text}
	changed := false
	tool := strings.TrimPrefix(label, "__tool_json@")
	if tool == "__tool_json" {
		tool = ""
	}
	inspect := func(text string, immutable bool, field string) (string, error) {
		checked, err := c.Check(WithRuleField(ctx, tool, field), tenant, text)
		if err != nil {
			return "", err
		}
		result.Issues = append(result.Issues, checked.Issues...)
		result.Compliant = result.Compliant && checked.Compliant
		result.Blocked = result.Blocked || checked.Blocked
		if checked.RedactedOutput != text {
			if immutable {
				result.ToolShapeRedactionRequired = true
				return text, nil
			}
			changed = true
		}
		return checked.RedactedOutput, nil
	}
	var visit func(any, int, string) (any, error)
	visit = func(value any, depth int, field string) (any, error) {
		if depth > 64 {
			return nil, fmt.Errorf("tool argument JSON depth exceeded")
		}
		switch v := value.(type) {
		case string:
			return inspect(v, false, field)
		case json.Number:
			_, err := inspect(v.String(), true, field)
			return value, err
		case bool:
			_, err := inspect(strconv.FormatBool(v), true, field)
			return value, err
		case nil:
			_, err := inspect("null", true, field)
			return value, err
		case map[string]any:
			for key, child := range v {
				// Keys and non-string scalars were visible to the old raw JSON
				// check too. Block redaction rather than silently change their type.
				if _, err := inspect(key, true, "__key"); err != nil {
					return nil, err
				}
				checked, err := visit(child, depth+1, key)
				if err != nil {
					return nil, err
				}
				v[key] = checked
			}
		case []any:
			for i, child := range v {
				checked, err := visit(child, depth+1, field)
				if err != nil {
					return nil, err
				}
				v[i] = checked
			}
		}
		return value, nil
	}
	root, err = visit(root, 0, "")
	if err != nil {
		return nil, err
	}
	if changed {
		encoded, err := json.Marshal(root)
		if err != nil {
			return nil, err
		}
		result.RedactedOutput = string(encoded)
	}
	return result, nil
}
