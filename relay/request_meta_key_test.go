package relay

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/auth"
	"github.com/kaixuan/llm-gateway-go/telemetry"
)

func TestKeyMetaFromKeyInfoFallbackPrefix(t *testing.T) {
	owner := "huangxt"
	info := &auth.KeyInfo{
		ID:              2,
		ApplicationCode: "hermes",
		OwnerUser:       &owner,
	}
	prefix, ownerOut, app := keyMetaFromKeyInfo(info)
	if prefix != "key#2" {
		t.Fatalf("prefix=%q want key#2", prefix)
	}
	if ownerOut != "huangxt" {
		t.Fatalf("owner=%q", ownerOut)
	}
	if app != "hermes" {
		t.Fatalf("app=%q", app)
	}
}

func TestApplyKeyInfoToRequestLog(t *testing.T) {
	owner := "alice"
	info := &auth.KeyInfo{
		ID:              9,
		TenantID:        "t1",
		ApplicationID:   3,
		ApplicationCode: "portal",
		KeyPrefix:       "sk-abcdef12****",
		OwnerUser:       &owner,
	}
	reqLog := &telemetry.RequestLogEntry{RequestID: "rid-1"}
	applyKeyInfoToRequestLog(reqLog, info)
	if reqLog.APIKeyPrefix == nil || *reqLog.APIKeyPrefix != "sk-abcdef12****" {
		t.Fatalf("prefix=%v", reqLog.APIKeyPrefix)
	}
	if reqLog.APIKeyOwnerUser == nil || *reqLog.APIKeyOwnerUser != "alice" {
		t.Fatalf("owner=%v", reqLog.APIKeyOwnerUser)
	}
	if reqLog.ApplicationCode == nil || *reqLog.ApplicationCode != "portal" {
		t.Fatalf("app=%v", reqLog.ApplicationCode)
	}
	if reqLog.APIKeyID == nil || *reqLog.APIKeyID != 9 {
		t.Fatalf("api_key_id=%v", reqLog.APIKeyID)
	}
}
