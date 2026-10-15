package outputcompliance

import "testing"

func TestR1034DBCheckerSeparatesRedactionCodeFromSecrets(t *testing.T) {
	c := &Checker{}
	policy := defaultPolicy("default")
	for _, text := range []string{
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env`,
		`sed -E -e 's#PASSWORD=.+#PASSWORD=***#g' -e 's|pwd=.*||' .env`,
		`sed 's/SECRET=.*/SECRET=[REDACTED]/;s/TOKEN=.*/TOKEN=[REDACTED:secret]/' .env`,
	} {
		if issues := c.detectSecrets(text, policy); len(issues) != 0 {
			t.Errorf("redaction code reported as secret: %q: %+v", text, issues)
		}
	}
	for _, text := range []string{
		`sed 's/PASSWORD=.*/TrulySensitive987!/' .env`,
		`sed 's/PASSWORD=TrulySensitive987!/PASSWORD=[REDACTED]/' .env`,
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env; password=TrulySensitive987!`,
	} {
		if issues := c.detectSecrets(text, policy); len(issues) == 0 {
			t.Errorf("real credential hidden by redaction syntax: %q", text)
		}
	}
}
