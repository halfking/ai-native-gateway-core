package envinjector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Injector coordinates decryption and output formatting.
type Injector struct {
	Decrypter SOPSDecrypter
	RepoRoot  string
}

// New creates an Injector with the default SOPS decrypter.
// repoRoot is the repository root containing .env.*.enc files.
func New(repoRoot string) *Injector {
	return &Injector{
		Decrypter: &DefaultSOPSDecrypter{},
		RepoRoot:  repoRoot,
	}
}

// Inject decrypts the target's .enc file and returns formatted output.
// format can be "eval" (default), "json", or "dotenv".
// If dryRun is true, only verify decryptability without emitting values.
func (inj *Injector) Inject(alias, format string, dryRun bool) (string, error) {
	target := FindTarget(alias)
	if target == nil {
		known := strings.Join(KnownAliases(), ", ")
		return "", fmt.Errorf("unknown target %q (known: %s)", alias, known)
	}

	encPath := filepath.Join(inj.RepoRoot, target.EncFile)
	if _, err := os.Stat(encPath); err != nil {
		return "", fmt.Errorf("encrypted file not found: %s", encPath)
	}

	raw, err := inj.Decrypter.Decrypt(encPath)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", target.EncFile, err)
	}

	if dryRun {
		return fmt.Sprintf("# OK: %s decrypts successfully (%d bytes)\n", target.EncFile, len(raw)), nil
	}

	vars, err := ParseDecrypted(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", target.EncFile, err)
	}

	if len(vars) == 0 {
		return "", fmt.Errorf("%s decrypted but contains no credential keys", target.EncFile)
	}

	switch format {
	case "json", "":
		if format == "json" {
			return FormatJSON(vars)
		}
		fallthrough
	case "eval":
		return FormatEval(vars), nil
	case "dotenv":
		return FormatDotenv(vars), nil
	default:
		return "", fmt.Errorf("unknown format %q (use: eval, json, dotenv)", format)
	}
}

// Verify checks that the target's .enc file is decryptable.
// Returns nil on success, error on failure.
func (inj *Injector) Verify(alias string) error {
	_, err := inj.Inject(alias, "eval", true)
	return err
}

// ListTargets emits SSH key mappings in KEY=PATH format.
func (inj *Injector) ListTargets() string {
	var b strings.Builder
	for _, t := range AllTargets() {
		keyPath := os.Getenv(t.SSHKeyEnv)
		if keyPath == "" {
			keyPath = t.SSHKeyDef
		}
		fmt.Fprintf(&b, "%s=%s\n", t.SSHKeyEnv, keyPath)
	}
	return b.String()
}

// EncryptTarget encrypts a plaintext .env.<alias> file to .env.<alias>.enc
// using sops. The plaintext file must exist; the .enc file is created.
func (inj *Injector) EncryptTarget(alias, plaintextPath string) error {
	target := FindTarget(alias)
	if target == nil {
		return fmt.Errorf("unknown target %q", alias)
	}

	if plaintextPath == "" {
		plaintextPath = filepath.Join(inj.RepoRoot, strings.Replace(target.EncFile, ".enc", "", 1))
	}

	if _, err := os.Stat(plaintextPath); err != nil {
		return fmt.Errorf("plaintext file not found: %s", plaintextPath)
	}

	encPath := filepath.Join(inj.RepoRoot, target.EncFile)
	cmd := fmt.Sprintf("sops --encrypt --in-place %s > %s", plaintextPath, encPath)
	_ = cmd // sops --encrypt reads .sops.yaml for rules

	// Use exec via shell for sops encryption
	return encryptWithSOPS(plaintextPath, encPath, inj.RepoRoot)
}

// MockDecrypter is a test-only decrypter that returns pre-set data.
type MockDecrypter struct {
	Data   []byte
	Err    error
	Called string
}

func (m *MockDecrypter) Decrypt(encPath string) ([]byte, error) {
	m.Called = encPath
	return m.Data, m.Err
}
