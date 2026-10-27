package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

// Recording consent (W1.3): tenant-level opt-in for persisting session bodies.
//
// Default OFF — usage/ledger always records; bodies/replay/forensics require
// explicit consent. Aligns with magpie Settings.GatewayConversations:
// disable-and-clear bumps a generation so an older settings snapshot cannot
// re-enable recording as if nothing was cleared, and in-flight writes that
// captured a stale generation are refused.

const (
	KeySessionRecordingConsent           = "session_recording_consent"
	KeySessionRecordingConsentGeneration = "session_recording_consent_generation"
	// Body retention hours — decoupled from request_logs lifecycle.
	KeySessionBodyRetentionHours = "session_body_retention_hours"
	// Soft store cap per tenant (MiB) for session_bodies_hot.
	KeySessionBodyStoreMaxMiB = "session_body_store_max_mib"
)

// RecordingConsentSpecs returns tenant-scoped recording-consent settings.
func RecordingConsentSpecs() []*Spec {
	return []*Spec{
		{
			Key:             KeySessionRecordingConsent,
			EnvName:         EnvNameAuto(KeySessionRecordingConsent),
			Type:            TypeBool,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         false,
			Description:     "会话正文录制同意",
			DescriptionLong: "默认关闭。关闭时只记账量、不落 session_bodies；回放/摘要/取证返回 recording_unavailable。打开后才写入正文。关闭或 clear 会抬升 generation，旧设置快照不能把同意「复活」成未清空状态。",
			HotReload:       true,
			DangerLevel:     Warning,
		},
		{
			Key:             KeySessionRecordingConsentGeneration,
			EnvName:         EnvNameAuto(KeySessionRecordingConsentGeneration),
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         1,
			Description:     "录制同意世代号",
			DescriptionLong: "disable/clear 时自增。写入路径校验世代，防止清库后在途请求回填正文。",
			HotReload:       true,
			DangerLevel:     Safe,
			Min:             floatPtr(1),
		},
		{
			Key:             KeySessionBodyRetentionHours,
			EnvName:         EnvNameAuto(KeySessionBodyRetentionHours),
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         168, // 7 days — magpie GatewayRetention
			Description:     "会话正文保留小时数",
			DescriptionLong: "与 request_logs 生命周期解耦。小时清理 worker 删除超过该窗口的 session_bodies（用量行不动）。",
			HotReload:       true,
			DangerLevel:     Warning,
			Min:             floatPtr(1),
			Max:             floatPtr(8760),
			Unit:            "小时",
		},
		{
			Key:             KeySessionBodyStoreMaxMiB,
			EnvName:         EnvNameAuto(KeySessionBodyStoreMaxMiB),
			Type:            TypeInt,
			Scope:           ScopeTenant,
			Category:        CategorySession,
			Default:         256, // magpie gatewayStoreMax
			Description:     "会话正文软容量上限",
			DescriptionLong: "租户 session_bodies_hot 软上限（MiB）。小时清理按最旧优先裁到上限以下。",
			HotReload:       true,
			DangerLevel:     Warning,
			Min:             floatPtr(16),
			Max:             floatPtr(8192),
			Unit:            "MiB",
		},
	}
}

// TestingConsentOverride, when non-nil, forces RecordingConsentEnabled for
// unit tests (production leaves it nil).
var TestingConsentOverride *bool

// RecordingConsentEnabled reports whether the tenant has opted in to body
// recording. Missing Global / unknown key / malformed value → false (fail
// closed on privacy: never write bodies without an affirmative yes).
func RecordingConsentEnabled(tenantID string) bool {
	if TestingConsentOverride != nil {
		return *TestingConsentOverride
	}
	return getTenantBool(tenantID, KeySessionRecordingConsent, false)
}

// RecordingConsentGeneration returns the current consent generation for tenant.
func RecordingConsentGeneration(tenantID string) int {
	return getTenantInt(tenantID, KeySessionRecordingConsentGeneration, 1)
}

// RecordingBodyRetentionHours returns the tenant body retention window.
func RecordingBodyRetentionHours(tenantID string) int {
	return getTenantInt(tenantID, KeySessionBodyRetentionHours, 168)
}

// RecordingBodyStoreMaxMiB returns the soft store cap for the tenant.
func RecordingBodyStoreMaxMiB(tenantID string) int {
	return getTenantInt(tenantID, KeySessionBodyStoreMaxMiB, 256)
}

var recordingGenMu sync.Mutex

// BumpRecordingConsentGeneration increments the generation via the tenant
// settings backend. Call on disable or clear so in-flight writers that
// captured the old generation cannot refill bodies.
func BumpRecordingConsentGeneration(store Backend, tenantID string) (int, error) {
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}
	recordingGenMu.Lock()
	defer recordingGenMu.Unlock()
	cur := RecordingConsentGeneration(tenantID)
	next := cur + 1
	if store == nil {
		return next, nil
	}
	if _, err := store.SetTenant(tenantID, KeySessionRecordingConsentGeneration, next); err != nil {
		return cur, err
	}
	return next, nil
}

func getTenantBool(tenantID, key string, fallback bool) bool {
	if Global == nil || tenantID == "" {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(ScopeTenant, key, tenantID)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: tenant bool unmarshal failed", "key", key, "tenant", tenantID, "error", err)
		return fallback
	}
	return v
}

func getTenantInt(tenantID, key string, fallback int) int {
	if Global == nil || tenantID == "" {
		return fallback
	}
	sp := Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := Global.EffectiveValue(ScopeTenant, key, tenantID)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		slog.Warn("settings: tenant int unmarshal failed", "key", key, "tenant", tenantID, "error", err)
		return fallback
	}
	return v
}
