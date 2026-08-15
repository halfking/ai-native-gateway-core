package durable

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/pashagolub/pgxmock/v4"
)

func TestStore_LoadSnapshot(t *testing.T) {
	store, mock := newMockStore(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-1", RequestHash: "request-hash"}
	envelope, keyID, err := secret.EncryptWithAAD([]byte(`{"model":"gpt-x"}`), store.kr, secret.AADDomainDurableRequest, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	mock.ExpectQuery(`SELECT id, tenant_id, request_id, request_hash`).
		WithArgs("task-1").
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "request_id", "request_hash", "snapshot_version", "encryption_key_id", "request_snapshot_ciphertext",
		}).AddRow("task-1", "tenant-1", "req-1", "request-hash", 1, keyID, envelope))

	snapshot, err := store.LoadSnapshot(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if string(snapshot.Body) != `{"model":"gpt-x"}` || snapshot.Version != 1 || snapshot.EncryptionKeyID != keyID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestStore_LoadSnapshot_FailClosed(t *testing.T) {
	kr := testKeyring(t)
	binding := secret.AADBinding{TenantID: "tenant-1", TaskID: "task-1", RequestHash: "request-hash"}
	envelope, keyID, err := secret.EncryptWithAAD([]byte("snapshot"), kr, secret.AADDomainDurableRequest, binding)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cases := []struct {
		name         string
		storeKeyring *secret.Keyring
		requestHash  string
		columnKeyID  string
		wantErr      error
	}{
		{"missing keyring", nil, "request-hash", keyID, secret.ErrAADNoKey},
		{"AAD request hash mismatch", kr, "other-hash", keyID, secret.ErrAADMismatch},
		{"key id column mismatch", kr, "request-hash", "other-key", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatalf("pgxmock: %v", err)
			}
			t.Cleanup(mock.Close)
			store := NewStore(mock, tc.storeKeyring)
			if tc.storeKeyring != nil {
				mock.ExpectQuery(`SELECT id, tenant_id, request_id, request_hash`).
					WithArgs("task-1").
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "tenant_id", "request_id", "request_hash", "snapshot_version", "encryption_key_id", "request_snapshot_ciphertext",
					}).AddRow("task-1", "tenant-1", "req-1", tc.requestHash, 1, tc.columnKeyID, envelope))
			}
			_, err = store.LoadSnapshot(context.Background(), "task-1")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err=%v, want %v", err, tc.wantErr)
				}
			} else if err == nil {
				t.Fatal("key id mismatch must fail closed")
			}
		})
	}
}
