package credentialtext

import (
	"strings"
	"testing"
)

func TestRedactionViewPreservesOffsetsAndOnlyKnownMasking(t *testing.T) {
	for _, script := range []string{
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/' .env`,
		`sed -E -e 's#PASSWORD=.+#PASSWORD=***#g' -e 's|pwd=.*||' .env`,
		`sed -i '' 's@^TOKEN=.*$@TOKEN=[REDACTED:secret]@' .env`,
		`cat .env | /usr/bin/sed "s/SECRET=.*/SECRET=<REDACTED>/"`,
		`sed 's/PASSWORD=[^;]*/PASSWORD=[MASKED]/' .env`,
	} {
		input := "中文 prefix\n" + script + "\npassword=ActualValue987!"
		view := DetectionText(input)
		if len(view) != len(input) || strings.Index(view, "password=ActualValue987!") != strings.Index(input, "password=ActualValue987!") {
			t.Fatal("byte offsets changed")
		}
		if view == input || !strings.HasSuffix(view, "password=ActualValue987!") {
			t.Errorf("masking view incorrect: %q", view)
		}
	}
	for _, text := range []string{
		`sed 's/PASSWORD=.*/ActualValue987!/'`,
		`sed 's/PASSWORD=ActualValue987!/PASSWORD=[REDACTED]/'`,
		`sed 's/PASSWORD=[^ActualValue987!]+/PASSWORD=[REDACTED]/'`,
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]'`,
		`sed 's/PASSWORD=.*/PASSWORD=[REDACTED]/e'`,
		`sed 's/PASSWORD=.*/{SENSITIVE:secret:999}/'`,
	} {
		if DetectionText(text) != text {
			t.Errorf("unknown/literal expression exempted: %q", text)
		}
	}
}
