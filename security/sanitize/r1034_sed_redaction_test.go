package sanitize

import (
	"context"
	"encoding/json"
	"testing"
)

func TestR1034SedRedactionCommandsRemainExecutable(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, detector := range map[string]*PatternDetector{"production": configured, "fallback": NewPatternDetector()} {
		t.Run(name, func(t *testing.T) {
			s, err := NewSanitizer(detector)
			if err != nil {
				t.Fatal(err)
			}
			checker := &outputSensitiveChecker{sanitizer: s, action: OutputMask}
			command := `sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/; s/SECRET=.*/SECRET=[REDACTED]/' .env`
			input, err := s.SanitizeInput(context.Background(), command)
			if err != nil {
				t.Fatal(err)
			}
			if input.SanitizedText != command {
				t.Error("redaction code was mistaken for a literal credential")
			}
			args, err := json.Marshal(map[string]string{"command": command})
			if err != nil {
				t.Fatal(err)
			}
			result, err := checker.CheckField(context.Background(), "default", "__tool_json@Bash", string(args))
			if err != nil {
				t.Fatal(err)
			}
			if result.Blocked {
				t.Fatal("legitimate sed redaction command was blocked")
			}
		})
	}
}

func TestR1034SedExpressionsCannotHideRealSecrets(t *testing.T) {
	c := r1028Checker(t)
	for _, command := range []string{
		`sed 's/PASSWORD=.*/TrulySensitive987!/' .env`,
		`export PASSWORD='$literal_password'`,
		`export PASSWORD='${DEPLOY_PASSWORD}'`,
		`sed 's/PASSWORD=.*/PASSWORD=TrulySensitive987!/' .env`,
		`sed 's/PASSWORD=[^TrulySensitive987!]+/PASSWORD=[REDACTED]/' .env`,
		`sed 's/PASSWORD=TrulySensitive987!/PASSWORD=[REDACTED]/' .env`,
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env; password=TrulySensitive987!`,
	} {
		args, _ := json.Marshal(map[string]string{"command": command})
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", string(args))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Blocked {
			t.Errorf("secret hidden by redaction syntax: %q", command)
		}
	}
}
