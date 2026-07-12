package tenantops

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestTenantID(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		tenantID string
		query    string
		wantID   string
		wantOK   bool
	}{
		{name: "tenant admin uses claim", role: "tenant_admin", tenantID: "tenant-a", query: "tenant-b", wantID: "tenant-a", wantOK: true},
		{name: "super admin requires explicit tenant", role: "super_admin", query: "tenant-a", wantID: "tenant-a", wantOK: true},
		{name: "super admin without tenant is forbidden", role: "super_admin", wantOK: false},
		{name: "regular user is forbidden", role: "user", tenantID: "tenant-a", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest("GET", "/?tenant="+tt.query, nil)
			ctx := e.NewContext(req, httptest.NewRecorder())
			ctx.Set("role", tt.role)
			ctx.Set("tenant_id", tt.tenantID)

			gotID, gotOK := tenantID(ctx)
			if gotID != tt.wantID || gotOK != tt.wantOK {
				t.Fatalf("tenantID() = (%q, %t), want (%q, %t)", gotID, gotOK, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestMaskLicenseKey(t *testing.T) {
	if got := maskLicenseKey("LICENSE-123456"); got != "LICE...3456" {
		t.Fatalf("maskLicenseKey() = %q", got)
	}
	if got := maskLicenseKey("short"); got != "********" {
		t.Fatalf("maskLicenseKey() short = %q", got)
	}
}
