package routing

import (
	"testing"
)

func TestGetMultiLevel_L1_SessionAndModel(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"
	credentialID := 999

	// Record success with session + model
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model, credentialID)

	// Should hit L1 (session + model)
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model)
	if !result.Found {
		t.Fatal("expected L1 hit, got miss")
	}
	if result.Level != StickyLevelSession {
		t.Errorf("expected L1 (StickyLevelSession), got level %d", result.Level)
	}
	if result.CredentialID != credentialID {
		t.Errorf("expected credentialID %d, got %d", credentialID, result.CredentialID)
	}
}

func TestGetMultiLevel_L2_ClientAndModel(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"
	credentialID := 999

	// Record success with session + model
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model, credentialID)

	// Query with different session but same model - should hit L2
	differentSession := "sess-456"
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, differentSession, model)
	if !result.Found {
		t.Fatal("expected L2 hit, got miss")
	}
	if result.Level != StickyLevelClientModel {
		t.Errorf("expected L2 (StickyLevelClientModel), got level %d", result.Level)
	}
	if result.CredentialID != credentialID {
		t.Errorf("expected credentialID %d, got %d", credentialID, result.CredentialID)
	}
}

func TestGetMultiLevel_L3_ClientBaseline(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"
	credentialID := 999

	// Record success with session + model
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model, credentialID)

	// Query with different session and different model - should hit L3
	differentSession := "sess-456"
	differentModel := "minimax-m2.7-quickspeed"
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, differentSession, differentModel)
	if !result.Found {
		t.Fatal("expected L3 hit, got miss")
	}
	if result.Level != StickyLevelClient {
		t.Errorf("expected L3 (StickyLevelClient), got level %d", result.Level)
	}
	if result.CredentialID != credentialID {
		t.Errorf("expected credentialID %d, got %d", credentialID, result.CredentialID)
	}
}

func TestGetMultiLevel_ModelSwitch_PreventsPollution(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"

	// User first uses claude
	claudeModel := "claude-opus-4-8"
	claudeCredID := 111
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, claudeModel, claudeCredID)

	// User switches to minimax in the same session
	minimaxModel := "minimax-m2.7-quickspeed"
	minimaxCredID := 222
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, minimaxModel, minimaxCredID)

	// Query claude - should still get claude credential (L1)
	claudeResult := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, claudeModel)
	if !claudeResult.Found || claudeResult.CredentialID != claudeCredID {
		t.Errorf("claude query should get claude credential %d, got %d (found=%v)",
			claudeCredID, claudeResult.CredentialID, claudeResult.Found)
	}

	// Query minimax - should get minimax credential (L1)
	minimaxResult := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, minimaxModel)
	if !minimaxResult.Found || minimaxResult.CredentialID != minimaxCredID {
		t.Errorf("minimax query should get minimax credential %d, got %d (found=%v)",
			minimaxCredID, minimaxResult.CredentialID, minimaxResult.Found)
	}
}

func TestGetMultiLevel_NoSessionID_FallsBackToL2(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"
	credentialID := 999

	// Record success with session + model
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model, credentialID)

	// Query without session ID - should hit L2 (client + model)
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, "", model)
	if !result.Found {
		t.Fatal("expected L2 hit, got miss")
	}
	if result.Level != StickyLevelClientModel {
		t.Errorf("expected L2 (StickyLevelClientModel), got level %d", result.Level)
	}
	if result.CredentialID != credentialID {
		t.Errorf("expected credentialID %d, got %d", credentialID, result.CredentialID)
	}
}

func TestGetMultiLevel_NoModel_FallsBackToL3(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"
	credentialID := 999

	// Record success with session + model
	cache.RecordSuccessMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model, credentialID)

	// Query without model - should hit L3 (client baseline)
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, "")
	if !result.Found {
		t.Fatal("expected L3 hit, got miss")
	}
	if result.Level != StickyLevelClient {
		t.Errorf("expected L3 (StickyLevelClient), got level %d", result.Level)
	}
	if result.CredentialID != credentialID {
		t.Errorf("expected credentialID %d, got %d", credentialID, result.CredentialID)
	}
}

func TestGetMultiLevel_Miss(t *testing.T) {
	cache := NewStickyCache()
	tenantID := "tenant-a"
	appID := 100
	apiKeyID := 200
	profile := "cursor"
	sessionID := "sess-123"
	model := "claude-opus-4-8"

	// No records - should miss
	result := cache.GetMultiLevel(tenantID, &appID, &apiKeyID, profile, sessionID, model)
	if result.Found {
		t.Error("expected miss, got hit")
	}
}
