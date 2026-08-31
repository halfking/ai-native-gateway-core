// Package main provides a diagnostic binary that prints the resolved
// gateway configuration for ops/debugging. By default, secrets are
// REDACTED — only fingerprints (length + first 8 bytes of SHA256) and
// non-sensitive fields are shown.
//
// To print the raw secret values (for incident response on a trusted
// operator workstation), pass --print-secrets on the command line OR set
// the CFG_DUMP_ALLOW_SECRETS=1 environment variable. Both gates are
// required to fail closed: missing either one keeps the redaction.
//
// This binary is intentionally separate from cmd/gateway so it cannot be
// served by the runtime HTTP path.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/kaixuan/llm-gateway-go/config"
)

func main() {
	printSecrets := flag.Bool("print-secrets", false, "Print raw secret values. Also requires CFG_DUMP_ALLOW_SECRETS=1.")
	flag.Parse()

	allowEnv := os.Getenv("CFG_DUMP_ALLOW_SECRETS") == "1"
	allow := *printSecrets && allowEnv

	cfg := config.Load()

	type secretField struct {
		label string
		value string
		env   string
	}
	fields := []secretField{
		{"SecretKey", cfg.SecretKey, ""},
		{"CredentialEncryptionKey", cfg.CredentialEncryptionKey, ""},
		{"DatabaseURL", cfg.DatabaseURL, ""},
		{"LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY", os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"), "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY"},
	}

	fmt.Printf("=== cfg_dump (allow_secrets=%v) ===\n", allow)
	for _, f := range fields {
		val := f.value
		if f.env != "" {
			val = os.Getenv(f.env)
		}
		if allow {
			fmt.Printf("%s=%q\n", f.label, val)
		} else {
			fmt.Printf("%s=<redacted len=%d sha256[:8]=%s>\n", f.label, len(val), fingerprint(val))
		}
	}
}

func fingerprint(s string) string {
	if s == "" {
		return "empty"
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
