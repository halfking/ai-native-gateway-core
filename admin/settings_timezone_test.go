package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestFormatOffset(t *testing.T) {
	cases := []struct {
		sec  int
		want string
	}{
		{8 * 3600, "UTC+8:00"},
		{0, "UTC+0:00"},
		{-5 * 3600, "UTC-5:00"},
		{(5*3600 + 30*60), "UTC+5:30"},
		{-(13*3600 + 45*60), "UTC-13:45"},
	}
	for _, c := range cases {
		if got := formatOffset(c.sec); got != c.want {
			t.Errorf("formatOffset(%d) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestSystemTimezoneChoicesIncludeEastEight(t *testing.T) {
	choices := systemTimezoneChoices()
	found := false
	for _, c := range choices {
		if c["name"] == "Asia/Shanghai" {
			found = true
		}
	}
	if !found {
		t.Fatal("choices must include Asia/Shanghai (default east-8)")
	}
}

func TestSystemTimezonePutRejectsInvalidIANA(t *testing.T) {
	h := &Handler{}
	body := `{"timezone": "Not/AZone"}`
	r := httptest.NewRequest(http.MethodPut, "/api/admin/system-timezone", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.systemTimezonePut(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unknown IANA timezone") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestSystemTimezonePutRejectsEmpty(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest(http.MethodPut, "/api/admin/system-timezone", strings.NewReader(`{"timezone": "  "}`))
	w := httptest.NewRecorder()
	h.systemTimezonePut(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422 for empty timezone", w.Code)
	}
}

func TestValidateTimezoneExportedParity(t *testing.T) {
	if err := settings.ValidateTimezoneExported("Asia/Shanghai"); err != nil {
		t.Fatalf("valid zone rejected: %v", err)
	}
	if err := settings.ValidateTimezoneExported("Bad/Zone"); err == nil {
		t.Fatal("invalid zone accepted")
	}
}
