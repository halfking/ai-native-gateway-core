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
	probeURL := flag.String("probe-url", "", "diagnostic: fetch this URL using credential from -probe-cred and dump raw response + parsed models")
	probeCred := flag.Int("probe-cred", 0, "credential id to use with -probe-url")
	chatProbe := flag.String("chat-probe", "", "diagnostic: test if this model is callable via credential from -probe-cred (detects unlisted-but-available models)")
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

	if *probeURL != "" && *probeCred > 0 {
		runProbe(h, *probeCred, *probeURL)
		return
	}
	if *chatProbe != "" && *probeCred > 0 {
		runChatProbe(h, *probeCred, *chatProbe)
		return
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

// runProbe fetches a single URL with a credential's auth headers and prints
// the HTTP status, the parsed model list, and the raw response body. Used to
// diagnose model-discovery mismatches (e.g. vendor exposes glm-5.2 under a
// different base path than the configured one).
func runProbe(h *admin.Handler, credID int, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	status, rawBody, models, err := h.DebugFetchVendorModelsRaw(ctx, credID, url)
	fmt.Printf("URL:    %s\n", url)
	fmt.Printf("CredID: %d\n", credID)
	fmt.Printf("Status: %d\n", status)
	if err != nil {
		fmt.Printf("Error:  %v\n", err)
	}
	fmt.Printf("Models (%d): %v\n", len(models), models)
	fmt.Println("--- raw body ---")
	fmt.Println(string(rawBody))
}

// runChatProbe tests whether a model is callable (even if not listed by /models)
// by issuing a minimal chat completion. A 200 means the model works.
func runChatProbe(h *admin.Handler, credID int, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	status, body, err := h.DebugChatProbe(ctx, credID, model)
	fmt.Printf("Model:  %s\n", model)
	fmt.Printf("CredID: %d\n", credID)
	fmt.Printf("Status: %d\n", status)
	if err != nil {
		fmt.Printf("Error:  %v\n", err)
	}
	fmt.Println("--- response body ---")
	fmt.Println(string(body))
}


