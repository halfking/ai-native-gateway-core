package outputcompliance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestR1034ToolFieldsDecodeJSONBeforeChecking(t *testing.T) {
	c := NewBuiltinChecker()
	for _, command := range []string{`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env`, `sed "s/PASSWORD=.*/PASSWORD=[REDACTED]/" .env`} {
		args, _ := json.Marshal(map[string]any{"command": command, "id": json.Number("9007222222222222")})
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", string(args))
		if err != nil || r.Blocked || r.RedactedOutput != string(args) {
			t.Fatalf("legitimate JSON changed: result=%+v err=%v", r, err)
		}
	}
	for _, args := range []string{`{"nested":[{"command":"password=ActualValue987!"}],"id":9007222222222222}`, `"password=ActualValue987!"`} {
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", args)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Issues) == 0 || strings.Contains(r.RedactedOutput, "ActualValue987!") || !json.Valid([]byte(r.RedactedOutput)) {
			t.Fatal("decoded secret escaped or JSON broken")
		}
		if strings.HasPrefix(args, "{") && !strings.Contains(r.RedactedOutput, "9007222222222222") {
			t.Fatal("integer precision lost")
		}
	}
	for _, args := range []string{`{"command":`, strings.Repeat("[", 66) + `"value"` + strings.Repeat("]", 66)} {
		if _, err := c.CheckField(context.Background(), "default", "__tool_json", args); err == nil {
			t.Fatal("malformed or unbounded JSON accepted")
		}
	}
}

func TestR1034ToolScalarAndKeyPIIRemainInspected(t *testing.T) {
	c := NewBuiltinChecker()
	for _, args := range []string{`{"phone":13800138000}`, `{"13800138000":"value"}`} {
		before, err := c.Check(context.Background(), "default", args)
		if err != nil || len(before.Issues) == 0 {
			t.Fatal("PII premise absent")
		}
		after, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", args)
		if err != nil || len(after.Issues) == 0 || !after.ToolShapeRedactionRequired {
			t.Fatalf("PII disappeared or immutable shape changed: result=%+v err=%v", after, err)
		}
	}
}

func TestR1034ToolJSONRejectsDuplicateKeys(t *testing.T) {
	c := NewBuiltinChecker()
	for _, args := range []string{`{"command":"password=ActualValue987!","command":"echo ok"}`, `{"nested":{"command":"password=ActualValue987!"},"nested":{}}`, `{"command":"password=ActualValue987!","comm\u0061nd":"echo ok"}`, `{"nested":[{"command":"password=ActualValue987!","command":"echo ok"}]}`} {
		before, err := c.Check(context.Background(), "default", args)
		if err != nil || len(before.Issues) == 0 {
			t.Fatal("raw detection premise absent")
		}
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", args)
		if err == nil && (r == nil || !r.Blocked) {
			t.Fatal("ambiguous JSON hid a previous value")
		}
	}
}
