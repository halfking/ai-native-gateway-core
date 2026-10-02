package sanitize

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func TestOperationalRulesFromDefaultsAndProductionConfig(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, detector := range map[string]*PatternDetector{"fallback": NewPatternDetector(), "configured": configured} {
		t.Run(name, func(t *testing.T) {
			s, _ := NewSanitizer(detector)
			samples := []struct{ text, value string }{
				{"帐号：ops-admin", "ops-admin"}, {"PASSWORD=short", "short"}, {"密码：短口令", "短口令"},
				{`password="two words and \"escaped\""`, "two words"},
				{`password="two words and \"escaped\""`, "escaped"},
				{"host=[2001:db8::]", "2001:db8::"}, {"host=[::]", "::"}, {"host=[fe80::1%eth0]", "fe80::1%eth0"},
				{"server=203.0.113.8", "203.0.113.8"}, {"host=[2001:db8::1234]", "2001:db8::1234"}, {"host=192.168.2.3", "192.168.2.3"},
				{"Authorization: Bearer abc.DEF-123", "abc.DEF-123"},
				{(&url.URL{Scheme: "postgres", User: url.UserPassword("operator", "synthetic-only"), Host: "db.example.invalid:5432", Path: "/app"}).String(), "operator:synthetic-only"},
				{strings.Join([]string{"-----BEGIN", "OPENSSH PRIVATE KEY-----"}, " ") + "\nYXNkZmdoamts\n-----END OPENSSH PRIVATE KEY-----", "YXNkZmdoamts"},
			}
			for _, sample := range samples {
				result, err := s.SanitizeInput(context.Background(), sample.text)
				if err != nil || strings.Contains(result.SanitizedText, sample.value) {
					t.Errorf("operational rule failed, err=%v", err)
					continue
				}
				restored, err := s.RestoreOutput(context.Background(), result.SanitizedText, result.SanitizeMap)
				if err != nil || restored != sample.text {
					t.Error("round trip did not preserve original value")
				}
			}
			for _, safe := range []string{"version 999.888.777.666", "normal ordinary text", "{SENSITIVE:secret:1}"} {
				result, err := s.SanitizeInput(context.Background(), safe)
				if err != nil || result.SanitizedText != safe {
					t.Errorf("safe text changed: %q", safe)
				}
			}
		})
	}
}
