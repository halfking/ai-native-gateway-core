package freediscovery

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// newMockDB constructs a (db, mock) pair. Shared by all test cases; each case sets its own expectations.
func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

// expectTenantGUC asserts the in-transaction RLS GUC setup statement.
func expectTenantGUC(mock sqlmock.Sqlmock, tenantID string) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").
		WithArgs().
		WillReturnResult(sqlmock.NewResult(0, 0)).
		WillDelayFor(0)
	_ = tenantID
}

func TestEscapeTenantID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"tenant-a", "tenant-a"},
		{"Tenant_1", "Tenant_1"},
		{"", "default"},
		{"default", "default"},
		{"bad; DROP TABLE x", "default"},
		{"bad'tenant", "default"},
		{strings.Repeat("a", 65), "default"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		if got := escapeTenantID(c.in); got != c.want {
			t.Errorf("escapeTenantID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTemplateManager_Create_RequiresKeyringForPlaintextKey(t *testing.T) {
	db, _ := newMockDB(t)
	// nil keyring: plaintext keys must be rejected (fail closed, never persist plaintext)
	m := NewTemplateManager(db, nil)
	enabled := true
	_, err := m.Create(context.Background(), "tenant-a", &CreateTemplateRequest{
		ProviderCode: "groq",
		DisplayName:  "Groq",
		BaseURL:      "https://api.groq.com/openai/v1",
		APIKey:       "gsk_real_key_material",
		Enabled:      &enabled,
	})
	if err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("plaintext key without keyring must fail, got %v", err)
	}
}

func TestTemplateManager_Create_ValidationFailureSkipsDB(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)
	_, err := m.Create(context.Background(), "tenant-a", &CreateTemplateRequest{
		ProviderCode: "BAD_CODE",
		DisplayName:  "x",
		BaseURL:      "https://a.com",
	})
	if err == nil {
		t.Fatal("invalid request must fail before touching DB")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL should have run: %v", err)
	}
}

func TestTemplateManager_Create_DuplicateKeyIsFriendlyError(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO provider_templates").
		WillReturnError(errors.New(`pq: duplicate key value violates unique constraint "provider_templates_provider_code_tenant_key"`))
	mock.ExpectRollback()

	_, err := m.Create(context.Background(), "tenant-a", &CreateTemplateRequest{
		ProviderCode: "groq",
		DisplayName:  "Groq",
		BaseURL:      "https://api.groq.com/openai/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate key should produce friendly error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestTemplateManager_Get_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT (.+) FROM provider_templates WHERE id = \\$1").
		WithArgs(int64(42)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := m.Get(context.Background(), "tenant-a", 42)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("want ErrTemplateNotFound, got %v", err)
	}
}

func TestTemplateManager_Get_RLSTenantGUCAlwaysSet(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	// Core contract: every read/write must SET LOCAL app.current_tenant inside the transaction,
	// otherwise the connection pool may reuse a connection carrying the previous tenant's GUC (lesson from freeresource audit round 3 C2).
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant = 'tenant-a'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM provider_templates").WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, _ = m.Get(context.Background(), "tenant-a", 1)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("RLS GUC contract violated: %v", err)
	}
}

func TestTemplateManager_Get_RoundTripFields(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	cols := []string{
		"id", "tenant_id", "provider_code", "display_name", "base_url", "api_type",
		"api_key_env", "api_key_encrypted", "models_endpoint",
		"quota_endpoint", "tos_url", "tos_verdict", "tos_notes",
		"enabled", "created_by", "created_at", "updated_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		7, "tenant-a", "groq", "Groq Free", "https://api.groq.com/openai/v1", "openai-completions",
		"$GROQ_API_KEY", nil, "/models",
		"", "https://groq.com/terms", "ok", "free tier documented",
		true, "admin", nil, nil,
	)

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("FROM provider_templates WHERE id = \\$1").WithArgs(int64(7)).WillReturnRows(rows)
	mock.ExpectRollback()

	tpl, err := m.Get(context.Background(), "tenant-a", 7)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tpl.ProviderCode != "groq" || tpl.DisplayName != "Groq Free" {
		t.Fatalf("basic fields lost: %+v", tpl)
	}
	if tpl.APIType != APITypeOpenAICompletions {
		t.Fatalf("api_type = %q", tpl.APIType)
	}
	if tpl.APIKeyEnv != "$GROQ_API_KEY" || tpl.HasCredential() != true {
		t.Fatalf("credential fields: env=%q has=%v", tpl.APIKeyEnv, tpl.HasCredential())
	}
	if tpl.TosVerdict != "ok" || tpl.TosURL != "https://groq.com/terms" {
		t.Fatalf("tos fields: %+v", tpl)
	}
	if !tpl.Enabled {
		t.Fatal("enabled lost")
	}
}

func TestTemplateManager_Delete_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows = not found
	mock.ExpectRollback()

	err := m.Delete(context.Background(), "tenant-a", 99)
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("want ErrTemplateNotFound, got %v", err)
	}
}

func TestTemplateManager_Delete_Success(t *testing.T) {
	db, mock := newMockDB(t)
	m := NewTemplateManager(db, nil)

	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL app\\.current_tenant").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM provider_templates WHERE id=\\$1").
		WithArgs(int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := m.Delete(context.Background(), "tenant-a", 7); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestTemplateManager_ResolveAPIKey_Keyless(t *testing.T) {
	db, _ := newMockDB(t)
	m := NewTemplateManager(db, nil)

	key, source, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{ID: 1})
	if err != nil || key != "" || source != "none" {
		t.Fatalf("keyless template: key=%q source=%q err=%v", key, source, err)
	}
}

func TestTemplateManager_ResolveAPIKey_FromEnv(t *testing.T) {
	db, _ := newMockDB(t)
	m := NewTemplateManager(db, nil)

	t.Setenv("FREEDISCOVERY_TEST_KEY", "sk-env-value")

	t.Run("dollar-prefixed", func(t *testing.T) {
		key, source, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{
			ID: 1, APIKeyEnv: "$FREEDISCOVERY_TEST_KEY",
		})
		if err != nil || key != "sk-env-value" || source != "env:FREEDISCOVERY_TEST_KEY" {
			t.Fatalf("key=%q source=%q err=%v", key, source, err)
		}
	})

	t.Run("bare-name", func(t *testing.T) {
		key, _, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{
			ID: 1, APIKeyEnv: "FREEDISCOVERY_TEST_KEY",
		})
		if err != nil || key != "sk-env-value" {
			t.Fatalf("key=%q err=%v", key, err)
		}
	})

	t.Run("missing-env-fails", func(t *testing.T) {
		_, _, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{
			ID: 1, APIKeyEnv: "$FREEDISCOVERY_DEFINITELY_UNSET_VAR",
		})
		if err == nil || !strings.Contains(err.Error(), "not set") {
			t.Fatalf("unset env must fail loudly, got %v", err)
		}
	})
}

func TestTemplateManager_ResolveAPIKey_EncryptedRoundTrip(t *testing.T) {
	// Real keyring encrypt/decrypt round trip: Create persists ciphertext, ResolveAPIKey decrypts back to plaintext.
	current := make([]byte, 32)
	for i := range current {
		current[i] = byte(i)
	}
	kr, err := secret.NewKeyring(map[string][32]byte{"v1": *(*[32]byte)(current)}, "v1")
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}

	db, _ := newMockDB(t)
	m := NewTemplateManager(db, kr)

	env, err := secret.EncryptAESGCM([]byte("sk-plain"), kr)
	if err != nil {
		t.Fatalf("EncryptAESGCM: %v", err)
	}

	key, source, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{
		ID: 1, APIKeyEncrypted: []byte(env),
	})
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if key != "sk-plain" || source != "encrypted" {
		t.Fatalf("round trip: key=%q source=%q", key, source)
	}
}

func TestTemplateManager_ResolveAPIKey_EncryptedNoKeyringFails(t *testing.T) {
	db, _ := newMockDB(t)
	m := NewTemplateManager(db, nil) // nil keyring

	_, _, err := m.ResolveAPIKey(context.Background(), &ProviderTemplate{
		ID: 1, APIKeyEncrypted: []byte("v1.someciphertext"),
	})
	if err == nil || !strings.Contains(err.Error(), "no keyring") {
		t.Fatalf("encrypted key without keyring must fail, got %v", err)
	}
}

func TestEnvLookup_ReadsProcessEnv(t *testing.T) {
	t.Setenv("FREEDISCOVERY_ENVLOOKUP_PROBE", "ok")
	if envLookup("FREEDISCOVERY_ENVLOOKUP_PROBE") != "ok" {
		t.Fatal("envLookup must read the real process env")
	}
	if os.Getenv("FREEDISCOVERY_DEFINITELY_UNSET_VAR_2") != "" {
		t.Fatal("precondition")
	}
}
