package envinjector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAlias(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"252", "252"},
		{"184", "252"}, // legacy
		{"71", "154"},  // legacy
		{"154", "154"},
		{"kaixuan-1", "kaixuan-1"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		got := ResolveAlias(tt.input)
		if got != tt.want {
			t.Errorf("ResolveAlias(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFindTarget(t *testing.T) {
	if tgt := FindTarget("252"); tgt == nil {
		t.Error("FindTarget(252) returned nil")
	}
	if tgt := FindTarget("184"); tgt == nil {
		t.Error("FindTarget(184) should resolve legacy alias to 252")
	}
	if tgt := FindTarget("nonexistent"); tgt != nil {
		t.Errorf("FindTarget(nonexistent) should be nil, got %+v", tgt)
	}
}

func TestParseDotenv(t *testing.T) {
	input := "SSH_PASS_252=secret123\nPG_PASS_252=dbpass\n# comment\n\nTOKEN=abc"
	vars := parseDotenv(input)
	if len(vars) != 3 {
		t.Fatalf("expected 3 vars, got %d: %v", len(vars), vars)
	}
	if vars["SSH_PASS_252"] != "secret123" {
		t.Errorf("SSH_PASS_252 = %q", vars["SSH_PASS_252"])
	}
	if vars["PG_PASS_252"] != "dbpass" {
		t.Errorf("PG_PASS_252 = %q", vars["PG_PASS_252"])
	}
	if vars["TOKEN"] != "abc" {
		t.Errorf("TOKEN = %q", vars["TOKEN"])
	}
}

func TestParseDotenvWithQuotes(t *testing.T) {
	input := `KEY1="double_quoted"` + "\n" + `KEY2='single_quoted'`
	vars := parseDotenv(input)
	if vars["KEY1"] != "double_quoted" {
		t.Errorf("KEY1 = %q, want double_quoted", vars["KEY1"])
	}
	if vars["KEY2"] != "single_quoted" {
		t.Errorf("KEY2 = %q, want single_quoted", vars["KEY2"])
	}
}

func TestParseDecrypted_Dotenv(t *testing.T) {
	raw := []byte("SSH_PASS=secret\nPG_PASS=dbpass\n")
	vars, err := ParseDecrypted(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(vars))
	}
}

func TestParseDecrypted_JSONWithData(t *testing.T) {
	raw := []byte(`{"data": "SSH_PASS=secret\nPG_PASS=dbpass\n"}`)
	vars, err := ParseDecrypted(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(vars))
	}
	if vars["SSH_PASS"] != "secret" {
		t.Errorf("SSH_PASS = %q", vars["SSH_PASS"])
	}
}

func TestParseDecrypted_JSONFlat(t *testing.T) {
	raw := []byte(`{"SSH_PASS_252": "secret123", "PG_PASS_252": "dbpass"}`)
	vars, err := ParseDecrypted(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(vars))
	}
}

func TestParseDecrypted_Empty(t *testing.T) {
	_, err := ParseDecrypted([]byte(""))
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestFormatEval(t *testing.T) {
	vars := map[string]string{
		"KEY1": "simple",
		"KEY2": "it's a test",
	}
	out := FormatEval(vars)
	if !contains(out, "export KEY1='simple'") {
		t.Errorf("missing export KEY1 in output:\n%s", out)
	}
	if !contains(out, `export KEY2='it'\''s a test'`) {
		t.Errorf("single quote escaping wrong in output:\n%s", out)
	}
}

func TestFormatJSON(t *testing.T) {
	vars := map[string]string{"KEY": "value"}
	out, err := FormatJSON(vars)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, `"KEY": "value"`) {
		t.Errorf("unexpected JSON output:\n%s", out)
	}
}

func TestFormatDotenv(t *testing.T) {
	vars := map[string]string{"KEY": "value"}
	out := FormatDotenv(vars)
	if out != "KEY=value\n" {
		t.Errorf("unexpected dotenv output: %q", out)
	}
}

func TestInject_WithMockDecrypter(t *testing.T) {
	tmpDir := t.TempDir()
	encFile := filepath.Join(tmpDir, ".env.252.enc")
	os.WriteFile(encFile, []byte("mock"), 0o600)

	mock := &MockDecrypter{
		Data: []byte(`{"data": "SSH_PASS_252=secret\nPG_PASS_252=dbpass\n"}`),
	}
	inj := &Injector{
		Decrypter: mock,
		RepoRoot:  tmpDir,
	}

	out, err := inj.Inject("252", "eval", false)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "export SSH_PASS_252='secret'") {
		t.Errorf("missing SSH_PASS_252 export:\n%s", out)
	}
}

func TestInject_DryRun(t *testing.T) {
	tmpDir := t.TempDir()
	encFile := filepath.Join(tmpDir, ".env.252.enc")
	os.WriteFile(encFile, []byte("mock"), 0o600)

	mock := &MockDecrypter{
		Data: []byte(`{"data": "SSH_PASS_252=secret\n"}`),
	}
	inj := &Injector{Decrypter: mock, RepoRoot: tmpDir}

	out, err := inj.Inject("252", "eval", true)
	if err != nil {
		t.Fatal(err)
	}
	if contains(out, "secret") {
		t.Error("dry-run should not emit values")
	}
	if !contains(out, "OK") {
		t.Error("dry-run should report OK")
	}
}

func TestInject_UnknownTarget(t *testing.T) {
	inj := New(".")
	_, err := inj.Inject("nonexistent", "eval", false)
	if err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestInject_MissingFile(t *testing.T) {
	inj := New(t.TempDir())
	_, err := inj.Inject("252", "eval", false)
	if err == nil {
		t.Error("expected error for missing .enc file")
	}
}

func TestListTargets(t *testing.T) {
	inj := New(".")
	out := inj.ListTargets()
	if !contains(out, "SSH_KEY_252=") {
		t.Error("missing SSH_KEY_252 in list output")
	}
	if !contains(out, "SSH_KEY_KAIXUAN_1=") {
		t.Error("missing SSH_KEY_KAIXUAN_1 in list output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
