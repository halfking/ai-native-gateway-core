// Command check-credentials scans the credentials table and classifies every
// secret_ciphertext by format. It is the diagnostic counterpart to the
// 2026-08-18 154 incident in which credential id=17 (apiclaude/130dao) was
// stored as 137 raw Fernet bytes instead of the standard v1:legacy:<b64>
// envelope, causing every Claude model request to fail with
// "cannot decrypt: unknown format".
//
// Usage:
//
//	check-credentials                          # human-readable scan (dry-run)
//	check-credentials -json                    # JSON output for CI / scripts
//	check-credentials -fix                     # rewrite raw-binary Fernet rows to v1:legacy:<b64>
//	check-credentials -fix -dry-run=false      # actually UPDATE the rows
//	check-credentials -credential 17           # scan one credential
//	check-credentials -tenant default          # scope to a tenant
//
// The tool depends only on config/db/secret (NOT admin) so it builds when
// admin's WIP files are mid-refactor. The fix path uses FernetKeyFromSecret
// to derive the same key the gateway uses, then re-wraps the recovered
// plaintext bytes into v1:legacy:<base64-url>.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// Format classifies a credential ciphertext byte sequence.
type Format string

const (
	FormatEmpty           Format = "empty"
	FormatV1AESEnvelope   Format = "v1-aes-envelope"   // v1:<kid>:<b64> AES-GCM
	FormatV1LegacyFernet  Format = "v1-legacy-fernet"  // v1:legacy:<b64> Fernet (URL-safe base64)
	FormatBareFernet      Format = "bare-fernet"       // gAAAAA... no envelope
	FormatRawFernetBinary Format = "raw-fernet-binary" // starts with 0x80 — UNREADABLE by DecryptAny
	FormatUnknown         Format = "unknown"
)

// Row is one credential's classification result. HexCiphertext is omitted
// from JSON output to avoid leaking the underlying key bytes.
type Row struct {
	ID            int    `json:"id"`
	ProviderID    int    `json:"provider_id"`
	Label         string `json:"label"`
	TenantID      string `json:"tenant_id"`
	SecretKid     string `json:"secret_kid"`
	CipherBytes   int    `json:"cipher_bytes"`
	Format        Format `json:"format"`
	DecryptOK     bool   `json:"decrypt_ok"`
	DecryptError  string `json:"decrypt_error,omitempty"`
	PlaintextHash string `json:"plaintext_hash,omitempty"` // sha256 hex prefix, never the key
	WillRewrite   bool   `json:"will_rewrite"`
	RewriteTarget string `json:"rewrite_target,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`

	// internal: hex form of secret_ciphertext for in-memory classification
	hexForm string
}

// ScanResult is the full report.
type ScanResult struct {
	ScannedAt   string         `json:"scanned_at"`
	TotalRows   int            `json:"total_rows"`
	ByFormat    map[Format]int `json:"by_format"`
	DecryptFail int            `json:"decrypt_failures"`
	Rewritten   int            `json:"rows_rewritten"`
	Rows        []Row          `json:"rows"`
}

func main() {
	var (
		credID  = flag.Int("credential", 0, "scan only this credential id (0 = all)")
		tenant  = flag.String("tenant", "", "limit to this tenant_id (default: all)")
		jsonOut = flag.Bool("json", false, "emit machine-readable JSON")
		fix     = flag.Bool("fix", false, "rewrite raw-binary Fernet rows to v1:legacy:<b64> envelopes")
		dryRun  = flag.Bool("dry-run", true, "with -fix, only print the planned UPDATE without writing")
		verbose = flag.Bool("v", false, "verbose: also print per-row details")
	)
	flag.Parse()

	cfg := config.Load()
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil || dbConn == nil || !dbConn.Enabled() {
		fmt.Fprintf(os.Stderr, "db connect failed: %v\n", err)
		os.Exit(2)
	}
	defer dbConn.Close()

	fernetKey, ferr := secret.FernetKeyFromSecret(cfg.SecretKey, cfg.CredentialEncryptionKey)
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "fernet key derivation failed: %v\n", ferr)
		fernetKey = nil
	}
	var keyring *secret.Keyring
	if k, kerr := secret.KeyringFromEnv(cfg.SecretKey, cfg.CredentialEncryptionKey); kerr == nil {
		keyring = k
	}

	rows, err := scanRows(context.Background(), dbConn, *credID, *tenant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %v\n", err)
		os.Exit(1)
	}

	result := ScanResult{
		ScannedAt: time.Now().UTC().Format(time.RFC3339),
		ByFormat:  make(map[Format]int),
		Rows:      rows,
	}

	for i := range result.Rows {
		row := &result.Rows[i]
		row.Format = classify(row)
		result.ByFormat[row.Format]++

		switch row.Format {
		case FormatV1AESEnvelope, FormatV1LegacyFernet, FormatBareFernet:
			ctStr := string(decodeHex(row.hexForm))
			pt, _, derr := secret.DecryptAny(ctStr, keyring, fernetKey)
			if derr != nil {
				row.DecryptOK = false
				row.DecryptError = derr.Error()
				result.DecryptFail++
			} else {
				row.DecryptOK = true
				row.PlaintextHash = shortHash(pt)
			}

		case FormatRawFernetBinary:
			// raw-binary Fernet CANNOT be decrypted through DecryptAny
			// (which expects base64-encoded ciphertext). Surface this
			// explicitly; with -fix, wrap back into v1:legacy:<b64>.
			row.DecryptError = "raw Fernet binary; DecryptAny requires base64 envelope"
			if *fix {
				if fernetKey == nil {
					row.DecryptError = "raw Fernet binary AND no fernet key configured; refusing to rewrite"
					result.DecryptFail++
				} else if pt, err := secret.DecryptFernet(decodeHex(row.hexForm), fernetKey); err != nil {
					row.DecryptError = "raw Fernet binary; raw Fernet decrypt also failed: " + err.Error()
					result.DecryptFail++
				} else {
					envelope := "v1:legacy:" + base64.RawURLEncoding.EncodeToString(decodeHex(row.hexForm))
					row.RewriteTarget = envelope
					row.WillRewrite = true
					if !*dryRun {
						if uerr := rewriteCiphertext(context.Background(), dbConn, row.ID, envelope); uerr != nil {
							row.DecryptError = "rewrite failed: " + uerr.Error()
							result.DecryptFail++
							row.WillRewrite = false
						} else {
							row.PlaintextHash = shortHash([]byte(pt))
							result.Rewritten++
						}
					}
				}
			} else {
				result.DecryptFail++
			}
		}
	}
	result.TotalRows = len(result.Rows)

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		if result.DecryptFail > 0 {
			os.Exit(1)
		}
		return
	}

	printHuman(result, *verbose, *fix, *dryRun)
	if result.DecryptFail > 0 {
		os.Exit(1)
	}
}

// classify inspects the raw ciphertext byte sequence and labels its format.
// Does NOT mutate state.
func classify(r *Row) Format {
	if r.CipherBytes == 0 {
		return FormatEmpty
	}
	rawBytes := decodeHex(r.hexForm)
	if len(rawBytes) == 0 {
		return FormatUnknown
	}
	// raw Fernet token always starts with version byte 0x80 — UNREADABLE
	// by DecryptAny/DecryptFernet (which expect base64-url encoded text).
	if rawBytes[0] == 0x80 {
		return FormatRawFernetBinary
	}
	ct := string(rawBytes)
	switch {
	case strings.HasPrefix(ct, "v1:legacy:"):
		return FormatV1LegacyFernet
	case strings.HasPrefix(ct, "v1:"):
		return FormatV1AESEnvelope
	case strings.HasPrefix(ct, "gAAAAA"):
		return FormatBareFernet
	}
	return FormatUnknown
}

func printHuman(r ScanResult, verbose, fix, dryRun bool) {
	fmt.Printf("=== check-credentials @ %s ===\n", r.ScannedAt)
	fmt.Printf("scanned %d rows\n\n", r.TotalRows)
	fmt.Println("format breakdown:")
	allFormats := []Format{
		FormatV1AESEnvelope, FormatV1LegacyFernet, FormatBareFernet,
		FormatRawFernetBinary, FormatEmpty, FormatUnknown,
	}
	for _, fmtType := range allFormats {
		n := r.ByFormat[fmtType]
		if n == 0 {
			continue
		}
		fmt.Printf("  %-20s %d\n", fmtType, n)
	}
	fmt.Printf("\ndecrypt failures: %d\n", r.DecryptFail)
	if fix {
		fmt.Printf("rows rewritten:   %d (dry-run=%v)\n", r.Rewritten, dryRun)
	}

	if !verbose && r.DecryptFail == 0 && r.ByFormat[FormatRawFernetBinary] == 0 {
		fmt.Println("\nall credentials look healthy")
		return
	}

	fmt.Println()
	for _, row := range r.Rows {
		mark := "OK "
		switch {
		case !row.DecryptOK:
			mark = "ERR"
		case row.Format == FormatRawFernetBinary:
			mark = "RAW"
		case row.Format == FormatUnknown:
			mark = "?? "
		}
		fmt.Printf("%s  id=%-4d label=%-20s format=%-20s bytes=%-4d",
			mark, row.ID, row.Label, row.Format, row.CipherBytes)
		if row.PlaintextHash != "" {
			fmt.Printf(" key_sha256_prefix=%s", row.PlaintextHash)
		}
		if row.DecryptError != "" {
			fmt.Printf("\n     err: %s", row.DecryptError)
		}
		if row.WillRewrite {
			fmt.Printf("\n     REWRITE -> %s", row.RewriteTarget)
		}
		fmt.Println()
	}
}

// --- SQL helpers -----------------------------------------------------------

func scanRows(ctx context.Context, dbConn *db.DB, credID int, tenant string) ([]Row, error) {
	var (
		clauses []string
		args    []any
	)
	if credID > 0 {
		args = append(args, credID)
		clauses = append(clauses, " AND c.id = $"+strconv.Itoa(len(args)))
	}
	if tenant != "" {
		args = append(args, tenant)
		clauses = append(clauses, " AND c.tenant_id = $"+strconv.Itoa(len(args)))
	}

	q := `
SELECT c.id, c.provider_id, c.tenant_id, c.label, COALESCE(c.secret_kid,''),
       octet_length(c.secret_ciphertext) AS ctlen,
       encode(c.secret_ciphertext, 'hex')  AS hexform,
       COALESCE(encode(c.updated_at, 'escape'), '') AS updated_at
FROM credentials c
WHERE 1=1` + strings.Join(clauses, "") + ` ORDER BY c.id`

	raw, err := dbConn.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	defer raw.Close()

	var rows []Row
	for raw.Next() {
		var r Row
		if err := raw.Scan(&r.ID, &r.ProviderID, &r.TenantID, &r.Label, &r.SecretKid,
			&r.CipherBytes, &r.hexForm, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		rows = append(rows, r)
	}
	return rows, raw.Err()
}

func rewriteCiphertext(ctx context.Context, dbConn *db.DB, id int, envelope string) error {
	_, err := dbConn.Pool().Exec(ctx, `
		UPDATE credentials
		SET secret_ciphertext = $1::bytea, updated_at = NOW()
		WHERE id = $2
	`, []byte(envelope), id)
	return err
}

// --- in-memory helpers -----------------------------------------------------

func decodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// shortHash returns the first 12 hex chars of sha256(plaintext). Used to
// prove the key round-trips through the gateway's decryption without
// leaking the key itself.
func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
