package main

// One-off tool: fix the broken self-check system api key row in 252 DB.
//
// Root cause: bg.EnsureSystemAPIKey previously stored key_hash = RAW plaintext,
// but the verifier looks up HMAC-SHA256(secret, raw). This tool decrypts the
// stored ciphertext to recover the raw key, recomputes the correct HMAC hash,
// and emits a SQL UPDATE so we can fix the DB in place (no restart needed).
//
// Usage:
//   go run ./cmd/tools/fix-selfcheck-keyhash -secret <SECRET_KEY> \
//       [-enc-key <CREDENTIAL_ENCRYPTION_KEY>] [-ciphertext <v1:legacy:...>]
//
// If -ciphertext is empty, it is read from stdin (the api_keys.key_ciphertext column).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	secretKey := flag.String("secret", "", "LLM_GATEWAY_SECRET_KEY (gateway HMAC + fernet source)")
	ciphertext := flag.String("ciphertext", "", "api_keys.key_ciphertext (v1:legacy:...). If empty, read from stdin.")
	explicitEnc := flag.String("enc-key", "", "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY (base64url 32 bytes), usually set")
	flag.Parse()

	if *secretKey == "" {
		fmt.Fprintln(os.Stderr, "error: -secret is required")
		os.Exit(2)
	}

	if *ciphertext == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read stdin:", err)
			os.Exit(1)
		}
		*ciphertext = string(b)
	}

	// Build the keyring the same way the gateway does, so DecryptAny's AES-GCM
	// path can handle the v1:legacy:<b64> envelope written by EncryptAESGCM.
	kr, krErr := secret.KeyringFromEnv(*secretKey, *explicitEnc)
	// Derive the 32-byte fernet key too (fallback path inside DecryptAny).
	fernetKey, _ := secret.FernetKeyFromSecret(*secretKey, *explicitEnc)

	var raw []byte
	if kr != nil {
		pt, _, err := secret.DecryptAny(*ciphertext, kr, fernetKey)
		if err != nil {
			fmt.Fprintln(os.Stderr, "decrypt (keyring):", err)
			os.Exit(1)
		}
		raw = pt
	} else {
		fmt.Fprintln(os.Stderr, "warning: no keyring built, trying fernet only:", krErr)
		pt, _, err := secret.DecryptAny(*ciphertext, nil, fernetKey)
		if err != nil {
			fmt.Fprintln(os.Stderr, "decrypt (fernet only):", err)
			os.Exit(1)
		}
		raw = pt
	}

	// Recompute the correct HMAC-SHA256 hash the verifier expects.
	mac := hmac.New(sha256.New, []byte(*secretKey))
	mac.Write([]byte(raw))
	correctHash := hex.EncodeToString(mac.Sum(nil))

	fmt.Println("-- recovered raw key prefix:", string(raw[:12])+"****")
	fmt.Printf("UPDATE api_keys SET key_hash = '%s' WHERE key_prefix LIKE 'sk-selfcheck%%' AND owner_user = 'self-check-worker';\n", correctHash)
	fmt.Printf("-- verify (should return 1 row): SELECT id FROM api_keys WHERE key_hash = '%s';\n", correctHash)
}
