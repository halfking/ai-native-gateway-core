package envinjector

import (
	"fmt"
	"os"
	"os/exec"
)

// writeFile is a small helper to keep the encrypt function readable.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// encryptWithSOPS runs `sops --encrypt` on the plaintext file and writes
// the encrypted envelope to encPath. The .sops.yaml in repoRoot determines
// the encryption rules (age recipient, path regex).
func encryptWithSOPS(plaintextPath, encPath, repoRoot string) error {
	if _, err := exec.LookPath("sops"); err != nil {
		return fmt.Errorf("sops binary not found in PATH: %w", err)
	}

	cmd := exec.Command("sops", "--encrypt", plaintextPath)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sops --encrypt %s failed: %w", plaintextPath, err)
	}

	if len(out) == 0 {
		return fmt.Errorf("sops produced empty output for %s", plaintextPath)
	}

	if err := writeFile(encPath, out); err != nil {
		return fmt.Errorf("write %s: %w", encPath, err)
	}
	return nil
}
