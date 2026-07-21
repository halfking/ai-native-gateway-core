package index

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestIndexUpsertAndQuery(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	idx := New(rdb, "ursm:v2:")
	ctx := context.Background()
	if err := idx.Upsert(ctx, Query{Tenant: "default", Canonical: "gpt", Profile: "", Modality: "text"}, Candidate{Score: 10, CredentialID: 1, RawModel: "gpt-4"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	out, err := idx.Query(ctx, Query{Tenant: "default", Canonical: "gpt", Profile: "", Modality: "text"}, 10)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(out) == 0 || out[0].CredentialID != 1 {
		t.Fatalf("unexpected result")
	}
}

func TestIndexRemove(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	idx := New(rdb, "ursm:v2:")
	ctx := context.Background()
	q := Query{Tenant: "t", Canonical: "m", Profile: "p", Modality: "x"}
	_ = idx.Upsert(ctx, q, Candidate{CredentialID: 1, RawModel: "m1"})
	_ = idx.Remove(ctx, q, Candidate{CredentialID: 1, RawModel: "m1"})
	out, _ := idx.Query(ctx, q, 10)
	if len(out) != 0 {
		t.Fatalf("remove did not take effect")
	}
}
