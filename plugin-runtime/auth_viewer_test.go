package pluginruntime

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestViewerWith_SuperAdmin(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := viewerWith(r, func(*http.Request) (AuthInfo, bool) {
		return AuthInfo{Super: true, PlatformOps: true}, true
	})
	if !v.IsSuper || !v.IsPlatformOps {
		t.Fatalf("got %+v", v)
	}
}

func TestViewerWith_TenantOnly(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := viewerWith(r, func(*http.Request) (AuthInfo, bool) {
		return AuthInfo{TenantPortal: true}, true
	})
	if v.IsSuper || v.IsPlatformOps || !v.IsTenantPortal {
		t.Fatalf("got %+v", v)
	}
}

func TestViewerWith_NoAuthReturnsZero(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	v := viewerWith(r, func(*http.Request) (AuthInfo, bool) { return AuthInfo{}, false })
	if v != (ViewerOpts{}) {
		t.Fatalf("got %+v", v)
	}
}

func TestViewerFromRequest_UsesDefaultExtractor(t *testing.T) {
	// Default extractor returns ok=false → zero ViewerOpts. Save/restore to keep test isolated.
	orig := defaultExtractor
	t.Cleanup(func() { defaultExtractor = orig })
	SetAuthExtractor(func(*http.Request) (AuthInfo, bool) {
		return AuthInfo{Super: true}, true
	})
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if v := ViewerFromRequest(r); !v.IsSuper {
		t.Fatalf("expected IsSuper, got %+v", v)
	}
}
