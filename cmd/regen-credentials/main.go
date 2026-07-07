// Regenerates credential ciphertexts in the database using the local test key.
// Usage: go run ./cmd/regen-credentials
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/kaixuan/llm-gateway-go/secret"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()

	// Use local test key - must match LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY env var
	explicitKey := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	fernetKey, err := secret.FernetKeyFromSecret("", explicitKey)
	if err != nil {
		log.Fatalf("FernetKeyFromSecret failed: %v", err)
	}
	fmt.Printf("Fernet key length: %d\n", len(fernetKey))

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatalf("DATABASE_URL not set")
	}

	dbPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("pgxpool.New failed: %v", err)
	}
	defer dbPool.Close()

	// First, let's see what credentials exist
	var creds []struct {
		ID               int
		ProviderID       int
		SecretCiphertext string
		Status           string
	}
	rows, err := dbPool.Query(ctx, `
		SELECT c.id, c.provider_id, encode(c.secret_ciphertext, 'escape'), c.status
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE p.display_name LIKE 'Local Mock%'
		ORDER BY c.id
	`)
	if err != nil {
		log.Fatalf("query credentials failed: %v", err)
	}
	for rows.Next() {
		var c struct {
			ID               int
			ProviderID       int
			SecretCiphertext string
			Status           string
		}
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.SecretCiphertext, &c.Status); err != nil {
			log.Fatalf("scan failed: %v", err)
		}
		creds = append(creds, c)
	}
	rows.Close()
	fmt.Printf("Found %d credentials\n", len(creds))

	// Generate new Fernet ciphertext for "test-api-key-{provider_id}"
	updated := 0
	for _, cred := range creds {
		apiKey := fmt.Sprintf("test-api-key-%d", cred.ProviderID)
		newCiphertext, err := secret.EncryptFernet([]byte(apiKey), fernetKey)
		if err != nil {
			log.Fatalf("EncryptFernet failed for cred %d: %v", cred.ID, err)
		}

		// Update DB with v1:legacy prefix (EncryptFernet returns base64-encoded bytes)
		fullCiphertext := "v1:legacy:" + string(newCiphertext)
		_, err = dbPool.Exec(ctx, `
			UPDATE credentials
			SET secret_ciphertext = $1, updated_at = $2
			WHERE id = $3
		`, fullCiphertext, time.Now(), cred.ID)
		if err != nil {
			log.Fatalf("update failed for cred %d: %v", cred.ID, err)
		}
		updated++
		fmt.Printf("Updated credential %d (provider %d): %s\n", cred.ID, cred.ProviderID, apiKey)
	}

	fmt.Printf("\nSuccessfully updated %d credentials\n", updated)
	fmt.Println("\nIMPORTANT: Restart the gateway to pick up new credentials")
}
