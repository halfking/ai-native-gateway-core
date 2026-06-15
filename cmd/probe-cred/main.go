// Command probe-cred is a self-contained diagnostic: it decrypts a credential's
// secret from the gateway DB and either prints the key or POSTs a minimal chat
// completion to a given model. It depends only on config/db/secret (not the
// admin package) so it builds even when admin's WIP files are mid-refactor.
//
// Usage (inside the gateway pod env):
//
//	probe-cred -cred 7                      # print decrypted api key
//	probe-cred -cred 7 -chat glm-5.2        # test if glm-5.2 is callable
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	credID := flag.Int("cred", 0, "credential id to decrypt")
	chatModel := flag.String("chat", "", "if set, POST a minimal chat completion to this model")
	flag.Parse()
	if *credID == 0 {
		fmt.Fprintln(os.Stderr, "-cred required")
		os.Exit(2)
	}

	cfg := config.Load()
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil || dbConn == nil || !dbConn.Enabled() {
		fmt.Fprintf(os.Stderr, "db connect failed: %v\n", err)
		os.Exit(2)
	}
	defer dbConn.Close()

	var (
		baseURL      string
		protocol     string
		catalogCode  string
		secretCipher []byte
	)
	err = dbConn.Pool().QueryRow(context.Background(), `
		SELECT COALESCE(p.base_url,''), COALESCE(p.protocol,''),
		       COALESCE(p.catalog_code,''), c.secret_ciphertext
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, *credID).Scan(&baseURL, &protocol, &catalogCode, &secretCipher)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query credential: %v\n", err)
		os.Exit(2)
	}

	fernetKey, _ := secret.FernetKeyFromSecret(cfg.SecretKey, cfg.CredentialEncryptionKey)
	var kr *secret.Keyring
	if k, kerr := secret.KeyringFromEnv(cfg.SecretKey, cfg.CredentialEncryptionKey); kerr == nil {
		kr = k
	}
	pt, _, derr := secret.DecryptAny(string(secretCipher), kr, fernetKey)
	if derr != nil {
		fmt.Fprintf(os.Stderr, "decrypt failed: %v\n", derr)
		os.Exit(2)
	}
	apiKey := string(pt)

	if *chatModel == "" {
		fmt.Println(apiKey)
		return
	}

	base := baseURL
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	url := base + "/chat/completions"
	payload := []byte(`{"model":"` + *chatModel + `","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	setHeaders(req, protocol, apiKey, catalogCode)
	req.Header.Set("Content-Type", "application/json")

	fmt.Printf("POST %s (model=%s)\n", url, *chatModel)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("Status: %d\n", resp.StatusCode)
	fmt.Println("--- body ---")
	fmt.Println(string(body))
}

func setHeaders(req *http.Request, protocol, apiKey, catalogCode string) {
	if protocol == "anthropic-messages" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
}
