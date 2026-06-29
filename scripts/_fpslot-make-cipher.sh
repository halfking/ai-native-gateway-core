#!/usr/bin/env bash
# Generate a Fernet ciphertext for the test secret.
# Usage: _fpslot-make-cipher.sh <base64-32byte-key> <plaintext>
# Stdout: hex string (no 0x prefix, no newline).
#
# Self-contained helper for verify-fpslot-fix.sh. Wraps an inline Go program
# so the verify script doesn't depend on /tmp/fpslot-make-cipher.go (which
# would need a writable /tmp AND a stable $GOPATH, neither of which we assume).

set -euo pipefail

KEY="$1"
PLAIN="$2"

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="/tmp/_fpslot-make-cipher-build-$$"
mkdir -p "$BUILD_DIR"
trap 'rm -rf "$BUILD_DIR"' EXIT

cat > "$BUILD_DIR/main.go" <<'GOEOF'
package main

import (
	"fmt"
	"os"

	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	key, err := secret.FernetKeyFromSecret("", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "key err:", err)
		os.Exit(2)
	}
	ct, err := secret.EncryptFernet([]byte(os.Args[2]), key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encrypt err:", err)
		os.Exit(3)
	}
	for _, b := range ct {
		fmt.Printf("%02x", b)
	}
}
GOEOF

(cd "$ROOT_DIR" && go run "$BUILD_DIR/main.go" "$KEY" "$PLAIN")