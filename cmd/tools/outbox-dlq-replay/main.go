// Command outbox-dlq-replay manually replays selected Gateway outbox DLQ events.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	_ "github.com/lib/pq"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (default: $DATABASE_URL)")
	endpoint := flag.String("endpoint", os.Getenv("ASM_INTERNAL_ENDPOINT"), "ai-session-manager events endpoint")
	secret := os.Getenv("AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET")
	eventID := flag.String("event-id", "", "replay one event_id")
	tenantID := flag.String("tenant-id", "", "replay DLQ events for one tenant")
	fromRaw := flag.String("from", "", "occurred_at lower bound, RFC3339 UTC")
	toRaw := flag.String("to", "", "occurred_at exclusive upper bound, RFC3339 UTC")
	limit := flag.Int("limit", 100, "maximum events to select (max 1000)")
	execute := flag.Bool("execute", false, "perform delivery; default is dry-run")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall replay timeout")
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("--dsn required (or set DATABASE_URL)")
	}
	if *execute && (strings.TrimSpace(*endpoint) == "" || strings.TrimSpace(secret) == "") {
		log.Fatal("--endpoint and AI_SESSION_MANAGER_GATEWAY_EVENT_SECRET are required for --execute")
	}
	if *execute && strings.TrimSpace(*eventID) == "" && strings.TrimSpace(*tenantID) == "" {
		log.Fatal("--execute requires --event-id or --tenant-id; time bounds alone cannot replay across tenants")
	}
	from, err := parseOptionalTime(*fromRaw)
	if err != nil {
		log.Fatalf("--from: %v", err)
	}
	to, err := parseOptionalTime(*toRaw)
	if err != nil {
		log.Fatalf("--to: %v", err)
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping database: %v", err)
	}

	deliverer := outbox.NewHTTPDeliverer(outbox.HTTPDelivererConfig{
		Endpoint:        *endpoint,
		Secret:          secret,
		AcceptDuplicate: false,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	})
	result, err := outbox.NewReplayer(db, deliverer).Replay(ctx, outbox.ReplayOptions{
		EventID:  strings.TrimSpace(*eventID),
		TenantID: strings.TrimSpace(*tenantID),
		From:     from,
		To:       to,
		Limit:    *limit,
		DryRun:   !*execute,
	})
	if err != nil {
		log.Fatal(err)
	}
	output, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("marshal result: %v", err)
	}
	fmt.Println(string(output))
}

func parseOptionalTime(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("must be RFC3339: %w", err)
	}
	if value.Location() != time.UTC {
		return nil, fmt.Errorf("must use UTC Z suffix")
	}
	value = value.UTC()
	return &value, nil
}
