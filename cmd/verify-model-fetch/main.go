// verify-model-fetch probes every active credential's /models endpoint using
// the same resolveModelsForCredential path as the admin "从供应商读取" button.
//
// Usage (on a host with DB + decrypt keys, e.g. 71/184 llm-gateway-go env):
//
//	go run ./cmd/verify-model-fetch
//	go run ./cmd/verify-model-fetch -provider 14
//	go run ./cmd/verify-model-fetch -upsert
//	go run ./cmd/verify-model-fetch -json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/secret"
)

func main() {
	providerID := flag.Int("provider", 0, "only test this provider id (0 = all)")
	jsonOut := flag.Bool("json", false, "emit JSON report")
	doUpsert := flag.Bool("upsert", false, "also run discoverAndUpsertForCredential (refresh path)")
	flag.Parse()

	cfg := config.Load()
	dbConn, err := db.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db connect failed: %v\n", err)
		os.Exit(2)
	}
	if dbConn == nil || !dbConn.Enabled() {
		fmt.Fprintf(os.Stderr, "database not configured\n")
		os.Exit(2)
	}
	defer dbConn.Close()

	fernetKey, _ := secret.FernetKeyFromSecret(cfg.SecretKey, cfg.CredentialEncryptionKey)
	h := admin.NewHandler(dbConn.Pool(), cfg.SecretKey, fernetKey)
	if kr, kerr := secret.KeyringFromEnv(cfg.SecretKey, cfg.CredentialEncryptionKey); kerr == nil {
		h.SetKeyring(kr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if *doUpsert {
		results, err := h.VerifyAllCredentialModelUpserts(ctx, *providerID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "upsert verify failed: %v\n", err)
			os.Exit(2)
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(results)
		} else {
			for _, r := range results {
				status := "FAIL"
				if r.OK {
					status = "OK"
				}
				line := fmt.Sprintf("[%s] provider=%d (%s) cred=%d %q upserted=%d failed=%d",
					status, r.ProviderID, r.ProviderCode, r.CredentialID, r.Label, r.Upserted, r.Failed)
				if r.Error != "" {
					line += " err=" + r.Error
				}
				fmt.Println(line)
			}
		}
		printUpsertSummary(results)
		return
	}

	results, err := h.VerifyAllCredentialModelFetches(ctx, *providerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		os.Exit(2)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		printFetchSummary(results)
		return
	}

	for _, r := range results {
		status := "FAIL"
		if r.OK {
			status = "OK"
		}
		line := fmt.Sprintf("[%s] provider=%d (%s) cred=%d %q url=%q source=%s count=%d",
			status, r.ProviderID, r.ProviderCode, r.CredentialID, r.Label, r.ResolvedURL, r.Source, r.ModelCount)
		if len(r.SampleModels) > 0 {
			line += fmt.Sprintf(" sample=%v", r.SampleModels)
		}
		if r.Error != "" {
			line += " err=" + r.Error
		}
		fmt.Println(line)
	}
	printFetchSummary(results)
}

func printFetchSummary(results []admin.CredentialModelFetchResult) {
	apiOK, manifestOnly, fail, decryptFail := 0, 0, 0, 0
	for _, r := range results {
		switch {
		case strings.HasPrefix(r.Error, "decrypt:"):
			decryptFail++
		case r.OK:
			if r.Source == "api" {
				apiOK++
			} else {
				manifestOnly++
			}
		case r.Source == "manifest" && r.ModelCount > 0:
			manifestOnly++
		default:
			fail++
		}
	}
	fmt.Fprintf(os.Stderr, "\nFETCH SUMMARY total=%d api_ok=%d manifest_only=%d hard_fail=%d decrypt_fail=%d\n",
		len(results), apiOK, manifestOnly, fail, decryptFail)
	if fail > 0 || decryptFail > 0 {
		os.Exit(1)
	}
}

func printUpsertSummary(results []admin.CredentialModelUpsertResult) {
	ok, fail := 0, 0
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	fmt.Fprintf(os.Stderr, "\nUPSERT SUMMARY total=%d ok=%d fail=%d\n", len(results), ok, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
