package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOmitBodyQueryAccepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "omit_body=1", raw: "omit_body=1", want: true},
		{name: "omit_body=true", raw: "omit_body=true", want: true},
		{name: "omit_body=0", raw: "omit_body=0", want: false},
		{name: "absent", raw: "", want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/api/logs/req-1?"+tc.raw, nil)
			got := req.URL.Query().Get("omit_body") == "1" || req.URL.Query().Get("omit_body") == "true"
			if got != tc.want {
				t.Fatalf("omit_body parse = %v, want %v (raw=%q values=%v)", got, tc.want, tc.raw, req.URL.Query())
			}
			if tc.raw != "" {
				q, err := url.ParseQuery(tc.raw)
				if err != nil {
					t.Fatal(err)
				}
				if q.Get("omit_body") == "" && tc.want {
					t.Fatal("expected query key present")
				}
			}
		})
	}
}
