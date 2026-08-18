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
//
// IDs are int64 to match the canonical schema's bigint columns; using int
// would overflow or filter inconsistently on platforms with 32-bit int or
// where credential IDs exceed math.MaxInt32.
type Row struct {
	ID            int64  `json:"id"`
	ProviderID    int64  `json:"provider_id"`
	Label         string `json:"label"`
	TenantID      string `json:"tenant_id"`
	SecretKid     string `json:"secret_kid"`
	CipherBytes   int64  `json:"cipher_bytes"`
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

// ScanResult is the full report. FormatAnomaly counts rows that cannot be
// decrypted by the running gateway (raw-fernet-binary + unknown + a v1
// envelope that failed DecryptAny). FormatUnknownEmpty counts rows whose
// ciphertext is empty bytea (these trip the CHECK constraint on next write
// and should not be reported as healthy).
type ScanResult struct {
	ScannedAt   string         `json:"scanned_at"`
	TotalRows   int            `json:"total_rows"`
	ByFormat    map[Format]int `json:"by_format"`
	DecryptFail int            `json:"decrypt_failures"`
	FormatAnom  int            `json:"format_anomalies"`
	EmptyRows   int            `json:"empty_ciphertext_rows"`
	Rewritten   int            `json:"rows_rewritten"`
	RewriteFail int            `json:"rewrite_failures"`
	Rows        []Row          `json:"rows"`
}

func main() {
	var (
		credID  = flag.Int64("credential", 0, "scan only this credential id (0 = all)")
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
				result.FormatAnom++
			} else {
				row.DecryptOK = true
				row.PlaintextHash = shortHash(pt)
			}

		case FormatRawFernetBinary:
			// raw-binary Fernet CANNOT be decrypted through DecryptAny
			// (which expects base64-encoded ciphertext). Surface this
			// explicitly; with -fix, wrap back into v1:legacy:<b64>.
			row.DecryptError = "raw Fernet binary; DecryptAny requires base64 envelope"
			result.FormatAnom++
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
						// Optimistic concurrency: re-check the original
						// hex form before writing. If a concurrent rotation
						// replaced the ciphertext between scanRows() and
						// now, the UPDATE affects 0 rows and we surface that
						// as a failure rather than silently overwriting.
						if uerr := rewriteCiphertext(context.Background(), dbConn,
							row.ID, row.hexForm, envelope); uerr != nil {
							row.DecryptError = "rewrite failed: " + uerr.Error()
							row.WillRewrite = false
							result.RewriteFail++
							result.DecryptFail++
						} else {
							row.PlaintextHash = shortHash([]byte(pt))
							result.Rewritten++
						}
					}
				}
			} else {
				result.DecryptFail++
			}

		case FormatUnknown:
			// Bytes that pass the CHECK constraint but cannot be decrypted.
			// Either an unknown envelope prefix, malformed body, or a value
			// written outside the encryptCred path. Either way: the gateway
			// will fail RevealAPIKey on this row.
			row.DecryptError = "unknown format; DecryptAny cannot classify or decrypt"
			result.FormatAnom++
			result.DecryptFail++

		case FormatEmpty:
			// Empty bytea: CHECK constraint 079 forbids this on new writes,
			// but historical rows may still exist (e.g. credentials in
			// import/pool_group workflows before the column was populated).
			// Surfacing them as anomalies lets operators fix or NULL them.
			result.EmptyRows++
			result.FormatAnom++
			row.DecryptError = "empty ciphertext; gateway returns no key"
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
	if r.hexForm == "" {
		// NULL ciphertext or empty bytea both come back as the empty string
		// from COALESCE(encode(...),''). Distinguish them via CipherBytes:
		// 0 with empty hex is either NULL or zero-length bytea (empty).
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
	fmt.Printf("\ndecrypt failures:    %d\n", r.DecryptFail)
	fmt.Printf("format anomalies:    %d (raw + unknown + failed decrypts)\n", r.FormatAnom)
	fmt.Printf("empty ciphertext:    %d\n", r.EmptyRows)
	if fix {
		fmt.Printf("rows rewritten:      %d (dry-run=%v)\n", r.Rewritten, dryRun)
		if r.RewriteFail > 0 {
			fmt.Printf("rewrite failures:    %d (concurrent change or DB error)\n", r.RewriteFail)
		}
	}

	if !verbose && r.DecryptFail == 0 && r.ByFormat[FormatRawFernetBinary] == 0 && r.ByFormat[FormatUnknown] == 0 {
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
		case row.Format == FormatEmpty:
			mark = "EMP"
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

func scanRows(ctx context.Context, dbConn *db.DB, credID int64, tenant string) ([]Row, error) {
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

	// NULL-aware projections: secret_ciphertext is NULL-able, so
	// octet_length/encode must be wrapped in COALESCE before being scanned
	// into Go scalars — otherwise the first NULL row aborts the whole scan.
	q := `
SELECT c.id, c.provider_id, c.tenant_id, c.label, COALESCE(c.secret_kid,''),
       COALESCE(octet_length(c.secret_ciphertext), 0) AS ctlen,
       COALESCE(encode(c.secret_ciphertext, 'hex'), '')  AS hexform,
       COALESCE(c.updated_at::text, '') AS updated_at
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
	if err := raw.Err(); err != nil {
		return nil, fmt.Errorf("scan rows: %w", err)
	}
	return rows, nil
}

// rewriteCiphertext atomically swaps the raw-Fernet ciphertext for the
// v1:legacy:<b64> envelope. oldHex is the value scanRows observed; the
// WHERE clause ensures we only overwrite rows whose ciphertext hasn't
// changed in the meantime. Returns ErrConcurrentRewrite (a sentinel the
// caller maps to a non-fatal per-row failure) when RowsAffected != 1.
func rewriteCiphertext(ctx context.Context, dbConn *db.DB, id int64, oldHex, envelope string) error {
	if oldHex == "" {
		return fmt.Errorf("refusing to rewrite: original ciphertext is empty (id=%d)", id)
	}
	oldBytes, err := hex.DecodeString(oldHex)
	if err != nil {
		return fmt.Errorf("decode original ciphertext hex: %w", err)
	}
	tx, err := dbConn.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	tag, err := tx.Exec(ctx, `
		UPDATE credentials
		SET secret_ciphertext = $1::bytea, updated_at = NOW()
		WHERE id = $2 AND secret_ciphertext = $3::bytea
	`, []byte(envelope), id, oldBytes)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errConcurrentRewrite
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

// errConcurrentRewrite signals that another writer changed
// secret_ciphertext between scan and fix. Surfaced per-row rather than as
// a fatal scan error so a single concurrent rotation does not abort the
// whole batch.
var errConcurrentRewrite = fmt.Errorf("concurrent ciphertext change detected")

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
