package licensing

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPgxStore_OfflineRequestStatusLifecycle(t *testing.T) {
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	store := NewPgxStore(pool)
	requestID := "status-lifecycle-" + time.Now().Format("20060102150405.000000")

	_, err = pool.Exec(ctx, `
		INSERT INTO offline_activation_requests (
			license_key, hardware_hash, instance_id, device_name, request_id, created_at, status
		) VALUES ($1, $2, $3, $4, $5, NOW(), 'pending')
	`, "LIC-TEST-STATUS", "hw-test-status", "instance-test-status", "device-test-status", requestID)
	if err != nil {
		t.Fatalf("insert offline request: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM offline_activation_requests WHERE request_id = $1`, requestID)
	}()

	if err := store.RejectOfflineRequest(ctx, requestID, "manual reject"); err != nil {
		t.Fatalf("RejectOfflineRequest pending row: %v", err)
	}

	var status string
	var rejectReason *string
	if err := pool.QueryRow(ctx, `
		SELECT status, reject_reason
		FROM offline_activation_requests
		WHERE request_id = $1
	`, requestID).Scan(&status, &rejectReason); err != nil {
		t.Fatalf("query rejected row: %v", err)
	}
	if status != "rejected" {
		t.Fatalf("status = %q, want rejected", status)
	}
	if rejectReason == nil || *rejectReason != "manual reject" {
		t.Fatalf("reject_reason = %v, want manual reject", rejectReason)
	}
}

func TestPgxStore_ApproveOfflineRequest_NotFound(t *testing.T) {
	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	store := NewPgxStore(pool)
	err = store.ApproveOfflineRequest(ctx, "does-not-exist", &SignedLicense{Data: []byte("x"), Signature: []byte("y")})
	if err == nil {
		t.Fatal("ApproveOfflineRequest expected error for missing request")
	}
}

func TestMarshalToBase64_RoundTripApprovalPayload(t *testing.T) {
	signed := &SignedLicense{Data: []byte("payload"), Signature: []byte("sig")}
	encoded, err := MarshalToBase64(signed)
	if err != nil {
		t.Fatalf("MarshalToBase64: %v", err)
	}
	decoded, err := UnmarshalFromBase64(encoded)
	if err != nil {
		t.Fatalf("UnmarshalFromBase64: %v", err)
	}
	got, _ := json.Marshal(decoded)
	want, _ := json.Marshal(signed)
	if string(got) != string(want) {
		t.Fatalf("roundtrip mismatch: got %s want %s", got, want)
	}
}
