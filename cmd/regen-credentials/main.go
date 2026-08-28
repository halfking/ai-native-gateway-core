// Regenerates credential ciphertexts in the database using the credential
// encryption key from the environment. The operator must set
// LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY (or LLM_GATEWAY_SECRET_KEY) — there is
// no hardcoded fallback so a leaked source tree cannot decrypt credentials.
// Usage: go run ./cmd/regen-credentials
package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	explicit := os.Getenv("LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY")
	secretKey := os.Getenv("LLM_GATEWAY_SECRET_KEY")
	fernetKey, err := secret.FernetKeyFromSecret(secretKey, explicit)
	if err != nil {
		logger.Error("credential encryption key not configured; set LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY or LLM_GATEWAY_SECRET_KEY")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		logger.Error("DATABASE_URL not set")
		os.Exit(1)
	}

	ctx := context.Background()
	dbPool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		logger.Error("pgxpool.New failed", "error", err.Error())
		os.Exit(1)
	}
	defer dbPool.Close()

	rows, err := dbPool.Query(ctx, `
		SELECT c.id, c.provider_id, encode(c.secret_ciphertext, 'escape'), c.status
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE p.display_name LIKE 'Local Mock%'
		ORDER BY c.id
	`)
	if err != nil {
		logger.Error("query credentials failed", "error", err.Error())
		os.Exit(1)
	}
	var creds []struct {
		ID               int
		ProviderID       int
		SecretCiphertext string
		Status           string
	}
	for rows.Next() {
		var c struct {
			ID               int
			ProviderID       int
			SecretCiphertext string
			Status           string
		}
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.SecretCiphertext, &c.Status); err != nil {
			rows.Close()
			logger.Error("scan failed", "error", err.Error())
			os.Exit(1)
		}
		creds = append(creds, c)
	}
	rows.Close()
	logger.Info("found credentials", "count", len(creds))

	updated := 0
	for _, cred := range creds {
		apiKey := "test-api-key-" + strconv.Itoa(cred.ProviderID)
		newCiphertext, err := secret.EncryptFernet([]byte(apiKey), fernetKey)
		if err != nil {
			logger.Error("EncryptFernet failed", "cred_id", cred.ID, "error", err.Error())
			os.Exit(1)
		}

		// Update DB with v1:legacy prefix (EncryptFernet returns base64-encoded bytes)
		fullCiphertext := "v1:legacy:" + string(newCiphertext)
		_, err = dbPool.Exec(ctx, `
			UPDATE credentials
			SET secret_ciphertext = $1, updated_at = $2
			WHERE id = $3
		`, fullCiphertext, time.Now(), cred.ID)
		if err != nil {
			logger.Error("update failed", "cred_id", cred.ID, "error", err.Error())
			os.Exit(1)
		}
		updated++
		// Do not log the API key — only the credential identity and provider.
		logger.Info("updated credential", "cred_id", cred.ID, "provider_id", cred.ProviderID)
	}

	logger.Info("regen-credentials complete", "updated", updated)
	logger.Info("restart gateway to pick up new credentials")
}
