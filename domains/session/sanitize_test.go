package session

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestManager_SaveAndGetSanitizeMap(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	sess, err := mgr.Create(ctx, 1, "tenant-t", "device-d")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	data := sanitize.SanitizeMap{
		"{SENSITIVE:phone:1}": "13800138000",
		"{SENSITIVE:email:1}": "test@example.com",
	}

	if err := mgr.SaveSanitizeMap(ctx, sess.SessionID, data); err != nil {
		t.Fatalf("SaveSanitizeMap: %v", err)
	}

	got, err := mgr.GetSanitizeMap(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSanitizeMap: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("GetSanitizeMap len = %d, want 2", len(got))
	}
	if got["{SENSITIVE:phone:1}"] != "13800138000" {
		t.Errorf("phone = %v, want 13800138000", got["{SENSITIVE:phone:1}"])
	}
	if got["{SENSITIVE:email:1}"] != "test@example.com" {
		t.Errorf("email = %v, want test@example.com", got["{SENSITIVE:email:1}"])
	}
}

func TestManager_SaveSanitizeMap_EmptyData(t *testing.T) {
	mgr, _ := newTestManager(t)

	if err := mgr.SaveSanitizeMap(context.Background(), "sess-1", sanitize.SanitizeMap{}); err != nil {
		t.Fatalf("SaveSanitizeMap empty: %v", err)
	}

	got, err := mgr.GetSanitizeMap(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetSanitizeMap: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetSanitizeMap len = %d, want 0", len(got))
	}
}

func TestManager_SaveSanitizeMap_Append(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	sess, _ := mgr.Create(ctx, 1, "t", "d")

	data1 := sanitize.SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"}
	if err := mgr.SaveSanitizeMap(ctx, sess.SessionID, data1); err != nil {
		t.Fatalf("SaveSanitizeMap 1: %v", err)
	}

	data2 := sanitize.SanitizeMap{"{SENSITIVE:email:1}": "test@example.com"}
	if err := mgr.SaveSanitizeMap(ctx, sess.SessionID, data2); err != nil {
		t.Fatalf("SaveSanitizeMap 2: %v", err)
	}

	got, err := mgr.GetSanitizeMap(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSanitizeMap: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("GetSanitizeMap len = %d, want 2; got=%v", len(got), got)
	}
}

func TestManager_GetSanitizeMap_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	got, err := mgr.GetSanitizeMap(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetSanitizeMap: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetSanitizeMap len = %d, want 0", len(got))
	}
}

func TestManager_DeleteSanitizeMap(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()

	sess, _ := mgr.Create(ctx, 1, "t", "d")
	data := sanitize.SanitizeMap{"{SENSITIVE:phone:1}": "13800138000"}

	if err := mgr.SaveSanitizeMap(ctx, sess.SessionID, data); err != nil {
		t.Fatalf("SaveSanitizeMap: %v", err)
	}

	if err := mgr.DeleteSanitizeMap(ctx, sess.SessionID); err != nil {
		t.Fatalf("DeleteSanitizeMap: %v", err)
	}

	got, err := mgr.GetSanitizeMap(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetSanitizeMap after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetSanitizeMap after delete len = %d, want 0", len(got))
	}
}

func TestManager_SaveSanitizeMap_NilReceiver(t *testing.T) {
	var mgr *Manager
	err := mgr.SaveSanitizeMap(context.Background(), "sess-1", sanitize.SanitizeMap{"k": "v"})
	if err != nil {
		t.Fatalf("SaveSanitizeMap nil receiver: %v", err)
	}
}

func TestManager_GetSanitizeMap_NilReceiver(t *testing.T) {
	var mgr *Manager
	got, err := mgr.GetSanitizeMap(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("GetSanitizeMap nil receiver: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("GetSanitizeMap nil receiver len = %d, want 0", len(got))
	}
}

func TestManager_DeleteSanitizeMap_NilReceiver(t *testing.T) {
	var mgr *Manager
	err := mgr.DeleteSanitizeMap(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("DeleteSanitizeMap nil receiver: %v", err)
	}
}
