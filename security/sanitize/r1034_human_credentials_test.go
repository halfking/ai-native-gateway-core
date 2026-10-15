package sanitize

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestR1034HumanCredentialDeclarationsRoundTrip(t *testing.T) {
	configured, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]*PatternDetector{"production": configured, "fallback": NewPatternDetector()} {
		t.Run(name, func(t *testing.T) {
			s, _ := NewSanitizer(d)
			cases := []struct{ text, secret string }{
				{"请将管理员帐号改成admin/R7#kQ92!，请继续", "R7#kQ92!"},
				{"管理员帐号改成admin/R7#kQ?92!，继续", "R7#kQ?92!"},
				{"管理员帐号改成`admin`/`R7#kQ92!`，继续", "R7#kQ92!"},
				{"密码=R7#kQ?92!，继续", "R7#kQ?92!"},
				{"管理员账号设置为 admin / R7#kQ92!。", "R7#kQ92!"},
				{"用户名/密码：admin／R7#kQ92!，请部署", "R7#kQ92!"},
				{"change admin account to admin/R7#kQ92!, continue", "R7#kQ92!"},
				{"密码是R7#kQ92!，继续", "R7#kQ92!"},
				{"管理员密码改为“含 空格/符号#”然后部署", "含 空格/符号#"},
				{"password is 'two words#'", "two words#"},
				{`password is "what?"`, "what?"},
				{"密码是“什么？”", "什么？"},
				{"管理员帐号改成admin/what?", "what?"},
				{`密码是"必填项"，请填写`, "必填项"},
				{`password='$literal_password'`, "$literal_password"},
				{"password set to R7#kQ92!", "R7#kQ92!"},
				{"**密码**：`R7#kQ92!`，继续", "R7#kQ92!"},
				{"账号/密码：\"ops\"/\"two words#/suffix\"，继续", "two words#/suffix"},
			}
			for _, tc := range cases {
				r, err := s.SanitizeInput(context.Background(), tc.text)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(r.SanitizedText, tc.secret) {
					t.Errorf("credential declaration was not sanitized: %q", tc.text)
					continue
				}
				fullValue := false
				for _, value := range r.SanitizeMap {
					fullValue = fullValue || value == tc.secret
				}
				if !fullValue {
					t.Errorf("entire secret not captured: %q", tc.text)
				}
				if len(r.SanitizeMap) == 0 {
					t.Fatal("missing reversible mapping")
				}
				restored, err := s.RestoreOutput(context.Background(), r.SanitizedText, r.SanitizeMap)
				if err != nil || restored != tc.text {
					t.Errorf("round trip changed original syntax: %q", tc.text)
				}
			}
		})
	}
}

func TestR1034CredentialPairKeepsSeparateTypes(t *testing.T) {
	s := productionConfiguredSanitizer(t)
	for _, input := range []string{"管理员帐号改成admin/R7#kQ92!，继续", "用户名/密码：admin/R7#kQ92!，继续"} {
		r, err := s.SanitizeInput(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.SanitizedText, "{SENSITIVE:account:") || !strings.Contains(r.SanitizedText, "{SENSITIVE:secret:") {
			t.Errorf("pair lost typed boundaries: %q", r.SanitizedText)
		}
	}
}

func TestR1034HumanCredentialAmbiguitiesRemainUnchanged(t *testing.T) {
	s := productionConfiguredSanitizer(t)
	for _, input := range []string{
		"~/work/docker-base-images", "docs/admin/password-policy.md", "https://admin.example.invalid/path", "admin/R7#kQ92!",
		"账号 ~/work/docker-base-images", "密码是什么呢？", "password is what ?", "密码是必填项，请填写。", "密码是什么？", "password is what?", "如何设置管理员密码", "密码长度为12位", "password policy requires 12 characters",
		"password=${DEPLOY_PASSWORD}", "密码为$DEPLOY_PASSWORD", "{SENSITIVE:secret:99}",
	} {
		r, err := s.SanitizeInput(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if r.SanitizedText != input {
			t.Errorf("non-literal declaration changed: %q -> %q", input, r.SanitizedText)
		}
	}
}

func TestR1034PairVariableRemainsReference(t *testing.T) {
	s := productionConfiguredSanitizer(t)
	r, err := s.SanitizeInput(context.Background(), "账号：admin/$DEPLOY_PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.SanitizedText, "{SENSITIVE:account:") || !strings.HasSuffix(r.SanitizedText, "/$DEPLOY_PASSWORD") || strings.Contains(r.SanitizedText, "{SENSITIVE:secret:") {
		t.Fatalf("pair reference misclassified: %q", r.SanitizedText)
	}
}

func TestR1034NaturalInputDoesNotWidenToolOutputRules(t *testing.T) {
	c := r1028Checker(t)
	for _, command := range []string{`echo 'password is incorrect'`, `echo 'token is expired'`, `echo '密码为必填项'`} {
		args, _ := json.Marshal(map[string]string{"command": command})
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", string(args))
		if err != nil {
			t.Fatal(err)
		}
		if r.Blocked {
			t.Errorf("status prose treated as an executable credential: %q", command)
		}
	}
	for _, value := range []string{"incorrect", "expired", "required"} {
		args, _ := json.Marshal(map[string]string{"command": "password=" + value})
		r, err := c.CheckField(context.Background(), "default", "__tool_json@Bash", string(args))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Blocked {
			t.Error("literal weak credential was exempted")
		}
	}
}

func TestR1034ExplicitCredentialActionsOverrideQuestionWords(t *testing.T) {
	s := productionConfiguredSanitizer(t)
	for _, prefix := range []string{"密码设置为", "密码设为", "密码更新为", "密码改为", "密码改成", "password set to ", "password changed to ", "password updated to "} {
		for _, secret := range []string{"what?", "which?"} {
			input := prefix + secret
			r, err := s.SanitizeInput(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, value := range r.SanitizeMap {
				found = found || value == secret
			}
			restored, restoreErr := s.RestoreOutput(context.Background(), r.SanitizedText, r.SanitizeMap)
			if !found || strings.Contains(r.SanitizedText, secret) || restoreErr != nil || restored != input {
				t.Errorf("explicit declaration not protected/round-tripped: %q", input)
			}
		}
	}
}
