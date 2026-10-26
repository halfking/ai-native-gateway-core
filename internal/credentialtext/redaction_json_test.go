package credentialtext

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONRedactionSyntaxPreservesSourceOffsets(t *testing.T) {
	for _, command := range []string{
		`sed "s/PASSWORD=.*/PASSWORD=[REDACTED]/"`,
		`sed 's/PASSWORD=.*/PASSWORD=[MASKED]/'`,
		`ssh build 'echo 中文😀; sed "s/SECRET=.*/SECRET=[MASKED]/"'`,
	} {
		encoded, _ := json.Marshal(map[string]string{"command": command + "; password=ActualValue987!"})
		escaped := strings.NewReplacer("PASSWORD", `\u0050ASSWORD`, "中文😀", `\u4e2d\u6587\ud83d\ude00`).Replace(string(encoded))
		for _, input := range []string{string(encoded), escaped} {
			input = "请调用工具：\n" + input
			view, spans := DetectionView(input)
			if len(view) != len(input) || len(spans) == 0 || !strings.Contains(view, "password=ActualValue987!") {
				t.Fatalf("JSON mask not isolated: %q", view)
			}
			for _, span := range spans {
				var source string
				if json.Unmarshal([]byte(`"`+input[span[0]:span[1]]+`"`), &source) != nil ||
					!strings.HasPrefix(source, "PASSWORD=") && !strings.HasPrefix(source, "SECRET=") {
					t.Fatalf("incorrect source boundary: %q", input[span[0]:span[1]])
				}
			}
			for i := range input {
				if view[i] != input[i] && view[i] != ' ' {
					t.Fatal("source bytes changed outside masking spans")
				}
			}
		}
	}
	for _, command := range []string{
		`sed "s/PASSWORD=.*/PASSWORD=[REDACTED]ActualValue987!/"`,
		`sed "s/PASSWORD=ActualValue987!/PASSWORD=[REDACTED]/"`,
		`sed "s/PASSWORD=.*/PASSWORD=[REDACTED]/e"`,
	} {
		encoded, _ := json.Marshal(command)
		if DetectionText(string(encoded)) != string(encoded) {
			t.Fatalf("JSON literal/unsupported expression exempted: %s", encoded)
		}
	}
	invalid := `"` + string([]byte{0xff, 0xff, 0xff}) + ` sed \"s/PASSWORD=.*/PASSWORD=[REDACTED]/\";password=ActualValue987!"`
	if DetectionText(invalid) != invalid {
		t.Fatal("invalid UTF-8 must remain visible")
	}
}
