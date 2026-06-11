package bg

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/urlutil"
	"github.com/kaixuan/llm-gateway-go/secret"
)

type CredentialCycler struct {
	db       *pgxpool.Pool
	encKey   []byte
	interval time.Duration
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewCredentialCycler(db *pgxpool.Pool, encKey []byte) *CredentialCycler {
	return &CredentialCycler{
		db:       db,
		encKey:   encKey,
		interval: 1 * time.Hour,
		done:     make(chan struct{}),
	}
}

func (c *CredentialCycler) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.run(ctx)
	slog.Info("credential cycler started", "interval", c.interval)
}

func (c *CredentialCycler) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	<-c.done
}

func (c *CredentialCycler) run(ctx context.Context) {
	defer close(c.done)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.cycleAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cycleAll(ctx)
		}
	}
}

func (c *CredentialCycler) cycleAll(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	rows, err := c.db.Query(timeoutCtx, `
		SELECT c.id, c.label, c.secret_ciphertext, p.base_url, p.protocol,
		       COALESCE(c.health_status, 'unknown'), c.availability_state, c.quota_state,
		       COALESCE(pc.models_endpoint_template, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.status = 'active'
		  AND c.lifecycle_status NOT IN ('suspended', 'retired', 'disabled')
		  AND (c.quota_state IS NULL OR c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted'))
		  AND c.availability_state = 'ready'
		  AND p.enabled = TRUE
		ORDER BY c.id
	`)
	if err != nil {
		slog.Warn("credential cycler: query failed", "error", err)
		return
	}
	defer rows.Close()

	checked := 0
	healthy := 0
	unreachable := 0

	for rows.Next() {
		var credID int
		var label, baseURL, protocol, modelsTemplate string
		var ciphertext []byte // bytea in DB, must be []byte for pgx scan
		var healthStatus, availState, quotaState *string

		if err := rows.Scan(&credID, &label, &ciphertext, &baseURL, &protocol, &healthStatus, &availState, &quotaState, &modelsTemplate); err != nil {
			continue
		}

		decrypted, decErr := decryptCred(string(ciphertext), c.encKey)
		if decErr != nil {
			c.updateHealth(ctx, credID, "error", "decrypt failed")
			continue
		}

		ok, errMsg := probeCredential(ctx, baseURL, modelsTemplate, decrypted)
		checked++
		if ok {
			healthy++
			c.updateHealth(ctx, credID, "healthy", "")
		} else {
			unreachable++
			status := "unreachable"
			if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") {
				status = "auth_failed"
			}
			c.updateHealth(ctx, credID, status, errMsg)
		}
	}

	slog.Info("credential cycler: cycle complete",
		"checked", checked,
		"healthy", healthy,
		"unreachable", unreachable,
	)

	c.cleanStickySessions(ctx)
}

func (c *CredentialCycler) updateHealth(ctx context.Context, credID int, status, errMsg string) {
	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c.db.Exec(execCtx, `
		UPDATE credentials
		SET health_status = $1, health_error = $2, health_checked_at = NOW(), health_source = 'probe'
		WHERE id = $3
	`, status, errMsg, credID)
}

func (c *CredentialCycler) cleanStickySessions(ctx context.Context) {
	execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tag, err := c.db.Exec(execCtx, `DELETE FROM sticky_sessions WHERE expires_at < now()`)
	if err != nil {
		slog.Debug("sticky session cleanup failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("credential cycler: cleaned sticky sessions", "count", tag.RowsAffected())
	}
}

func decryptCred(ciphertext string, encKey []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	return secret.DecryptFernet([]byte(ciphertext), encKey)
}

func probeCredential(ctx context.Context, baseURL, modelsTemplate, apiKey string) (bool, string) {
	if baseURL == "" {
		return false, "empty base URL"
	}

	probeURL := urlutil.BuildModelsURL(baseURL, modelsTemplate)
	if probeURL == "" {
		// manifest-only supplier: cannot verify via /models; trust provider catalog
		return true, "manifest-only, skipped probe"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", probeURL, nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, ""
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return false, httpStatusToMsg(resp.StatusCode, string(body))
}

func httpStatusToMsg(statusCode int, body string) string {
	msg := strings.TrimSpace(body)
	if len(msg) > 100 {
		msg = msg[:100]
	}
	switch statusCode {
	case 401, 403:
		return "auth error"
	case 429:
		return "rate limited"
	case 500, 502, 503, 504:
		return "upstream error"
	}
	return msg
}

type credentialProbeResult struct {
	CredentialID int    `json:"credential_id"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

func (c *CredentialCycler) probeOne(ctx context.Context, credID int) credentialProbeResult {
	result := credentialProbeResult{CredentialID: credID}

	execCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var ciphertext []byte // bytea in DB, must be []byte for pgx scan
	var baseURL, protocol, modelsTemplate string
	err := c.db.QueryRow(execCtx, `
		SELECT c.secret_ciphertext, p.base_url, COALESCE(p.protocol,'openai-completions'),
		       COALESCE(pc.models_endpoint_template, '')
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.id = $1 AND c.status = 'active'
	`, credID).Scan(&ciphertext, &baseURL, &protocol, &modelsTemplate)
	if err != nil {
		return credentialProbeResult{CredentialID: credID, Status: "error", Error: "not found"}
	}

	decrypted, decErr := decryptCred(string(ciphertext), c.encKey)
	if decErr != nil {
		return credentialProbeResult{CredentialID: credID, Status: "error", Error: "decrypt failed"}
	}

	ok, errMsg := probeCredential(ctx, baseURL, modelsTemplate, decrypted)
	if ok {
		result.Status = "healthy"
		c.updateHealth(ctx, credID, "healthy", "")
	} else {
		status := "unreachable"
		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") {
			status = "auth_failed"
		}
		result.Status = status
		result.Error = errMsg
		c.updateHealth(ctx, credID, status, errMsg)
	}
	return result
}