package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeInstaller struct {
	gotPlugin string
	gotVer    string
	err       error
}

func (f *fakeInstaller) Install(ctx context.Context, pluginID, version string) error {
	f.gotPlugin = pluginID
	f.gotVer = version
	return f.err
}

func TestPluginInstallHandlerHappy(t *testing.T) {
	fi := &fakeInstaller{}
	handler := makePluginInstallHandler(fi)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install",
		strings.NewReader(`{"plugin_id":"asm","version":"0.2.0"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fi.gotPlugin != "asm" || fi.gotVer != "0.2.0" {
		t.Errorf("Install args = %q/%q, want asm/0.2.0", fi.gotPlugin, fi.gotVer)
	}
}

func TestPluginInstallHandlerBadBody(t *testing.T) {
	fi := &fakeInstaller{}
	handler := makePluginInstallHandler(fi)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install",
		strings.NewReader(`not json`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad body status = %d, want 400", rec.Code)
	}
}

func TestPluginInstallHandlerMissingFields(t *testing.T) {
	fi := &fakeInstaller{}
	handler := makePluginInstallHandler(fi)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install",
		strings.NewReader(`{"plugin_id":"asm"}`)) // no version
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing fields status = %d, want 400", rec.Code)
	}
}

func TestPluginInstallHandlerInstallError(t *testing.T) {
	fi := &fakeInstaller{err: errors.New("catalog unreachable")}
	handler := makePluginInstallHandler(fi)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install",
		strings.NewReader(`{"plugin_id":"asm","version":"0.2.0"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("install error status = %d, want 500", rec.Code)
	}
}

func TestPluginInstallHandlerDisabledWhenNil(t *testing.T) {
	handler := makePluginInstallHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugins/install",
		strings.NewReader(`{"plugin_id":"asm","version":"0.2.0"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil installer status = %d, want 503", rec.Code)
	}
}
