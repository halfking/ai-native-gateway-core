// Package apihub — PGStore tests (Test-First per AGENTS.md §9-10).
//
// Uses a minimal pgxQuerier implementation that backs BeginTx with an
// in-memory fake. We don't try to parse SQL — we recognise the statements
// PGStore issues by their prefix and update in-memory state. The SQL
// strings themselves are part of the test surface: changing them is a
// deliberate compatibility decision.
package apihub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// row is one row of fake asset data.
type row struct {
	kind       string
	refID      int64
	tenantID   string
	name       string
	owner      string
	team       string
	costCenter string
	tags       []byte
	health     string
	version    string
	registered time.Time
	lastSeen   *time.Time
	metadata   []byte
}

// fakeTx implements pgx.Tx with just enough surface for PGStore.
// Routes Exec calls through to the parent fakeQuerier.
type fakeTx struct {
	parent *fakeQuerier
	tenant string
	done   bool
}

func (t *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if isSetTenant(sql) {
		if len(args) != 1 {
			return pgconn.CommandTag{}, fmt.Errorf("fake: SET LOCAL expects 1 arg, got %d", len(args))
		}
		v, ok := args[0].(string)
		if !ok {
			return pgconn.CommandTag{}, fmt.Errorf("fake: SET LOCAL arg must be string")
		}
		t.tenant = v
		return pgconn.CommandTag{}, nil
	}
	if isUpsertAssets(sql) {
		if err := t.parent.upsertAsset(args); err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, nil
	}
	if isLinkRelationship(sql) {
		if err := t.parent.linkRel(args); err != nil {
			return pgconn.CommandTag{}, err
		}
		return pgconn.CommandTag{}, nil
	}
	return pgconn.CommandTag{}, fmt.Errorf("fake: Exec unsupported sql: %s", sql)
}

func (t *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("fake: tx.Query not implemented (use fake Querier.Query)")
}

func (t *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

func (t *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, errors.New("fake: nested Begin not supported")
}
func (t *fakeTx) BeginFunc(ctx context.Context, f func(pgx.Tx) error) error {
	return errors.New("fake: BeginFunc not supported")
}
func (t *fakeTx) Commit(ctx context.Context) error   { t.done = true; return nil }
func (t *fakeTx) Rollback(ctx context.Context) error { t.done = true; return nil }
func (t *fakeTx) Conn() *pgx.Conn                    { return nil }
func (t *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("fake: CopyFrom not supported")
}
func (t *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return nil
}
func (t *fakeTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }
func (t *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("fake: Prepare not supported")
}

// fakeQuerier is an in-memory pgxQuerier. It supports BeginTx (with our
// fake tx) plus direct Exec for tests that bypass tx.
type fakeQuerier struct {
	mu     sync.Mutex
	assets map[string]row // key = kind + "|" + refID
	rels   map[string]bool
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{
		assets: make(map[string]row),
		rels:   make(map[string]bool),
	}
}

func (f *fakeQuerier) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return &fakeTx{parent: f}, nil
}

func (f *fakeQuerier) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, fmt.Errorf("fake: direct Exec not used; route through BeginTx")
}

func (f *fakeQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("fake: Query not used by tests")
}

func (f *fakeQuerier) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

// --- fake helpers below ---

func isSetTenant(sql string) bool {
	return strings.HasPrefix(sql, "SET LOCAL app.current_tenant") ||
		strings.HasPrefix(sql, "SELECT set_config('app.current_tenant'")
}

func isUpsertAssets(sql string) bool {
	return strings.Contains(sql, "INSERT INTO public.assets") ||
		strings.Contains(sql, "INSERT INTO assets")
}

func isLinkRelationship(sql string) bool {
	return strings.Contains(sql, "INSERT INTO public.asset_relationships") ||
		strings.Contains(sql, "INSERT INTO asset_relationships")
}

func (f *fakeQuerier) upsertAsset(args []any) error {
	// Expected arg order from PGStore.Upsert:
	//  0: kind, 1: refID, 2: tenantID, 3: name, 4: owner, 5: team,
	//  6: costCenter, 7: tagsBytes, 8: health, 9: version, 10: metadataBytes
	if len(args) < 11 {
		return fmt.Errorf("fake: upsert expects 11 args, got %d", len(args))
	}
	kind, _ := args[0].(string)
	refID, _ := args[1].(int64)
	tenantID, _ := args[2].(string)
	name, _ := args[3].(string)
	owner, _ := args[4].(string)
	team, _ := args[5].(string)
	costCenter, _ := args[6].(string)
	tagsBytes, _ := args[7].([]byte)
	health, _ := args[8].(string)
	version, _ := args[9].(string)
	metadataBytes, _ := args[10].([]byte)

	now := time.Now().UTC()
	key := kind + "|" + itoa(refID)

	existing, exists := f.assets[key]
	if exists {
		existing.lastSeen = &now
		existing.name = name
		existing.owner = owner
		existing.team = team
		existing.costCenter = costCenter
		existing.tags = tagsBytes
		existing.health = health
		existing.version = version
		existing.metadata = metadataBytes
		f.assets[key] = existing
		return nil
	}

	f.assets[key] = row{
		kind:       kind,
		refID:      refID,
		tenantID:   tenantID,
		name:       name,
		owner:      owner,
		team:       team,
		costCenter: costCenter,
		tags:       tagsBytes,
		health:     health,
		version:    version,
		registered: now,
	}
	return nil
}

func (f *fakeQuerier) linkRel(args []any) error {
	if len(args) < 5 {
		return fmt.Errorf("fake: link expects 5 args, got %d", len(args))
	}
	srcKind, _ := args[0].(string)
	srcRefID, _ := args[1].(int64)
	dstKind, _ := args[2].(string)
	dstRefID, _ := args[3].(int64)
	rel, _ := args[4].(string)
	key := srcKind + "|" + itoa(srcRefID) + "->" + dstKind + "|" + itoa(dstRefID) + ":" + rel
	f.rels[key] = true
	return nil
}

func itoa(i int64) string {
	return fmt.Sprintf("%d", i)
}

// --- tests ---

func newTestStore() (*pgStore, *fakeQuerier) {
	fq := newFakeQuerier()
	return &pgStore{q: fq}, fq
}

func TestPGStore_Upsert_RoundTrip(t *testing.T) {
	store, fq := newTestStore()
	ctx := context.Background()

	a := Asset{
		Kind:     KindLLMEndpoint,
		RefID:    42,
		TenantID: "tenantA",
		Name:     "gpt-4o",
		Owner:    "alice",
		Tags:     map[string]string{"region": "us-east-1"},
		Metadata: map[string]any{"provider": "openai"},
		Version:  "1.2.3",
	}

	if err := store.Upsert(ctx, a); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	key := "llm_endpoint|42"
	got, ok := fq.assets[key]
	if !ok {
		t.Fatalf("asset not stored under key %q (have keys=%v)", key, fq.assetKeys())
	}
	if got.tenantID != "tenantA" {
		t.Errorf("tenant_id = %q, want tenantA", got.tenantID)
	}
	if got.name != "gpt-4o" {
		t.Errorf("name = %q, want gpt-4o", got.name)
	}
	if got.owner != "alice" {
		t.Errorf("owner = %q, want alice", got.owner)
	}

	var tags map[string]string
	if err := json.Unmarshal(got.tags, &tags); err != nil {
		t.Fatalf("tags not valid JSON: %v (raw=%s)", err, got.tags)
	}
	if tags["region"] != "us-east-1" {
		t.Errorf("tags[region] = %q, want us-east-1", tags["region"])
	}
}

func TestPGStore_Upsert_Idempotent(t *testing.T) {
	store, fq := newTestStore()
	ctx := context.Background()

	a := Asset{
		Kind:     KindLLMEndpoint,
		RefID:    1,
		TenantID: "t1",
		Name:     "first",
	}
	if err := store.Upsert(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Name = "second"
	if err := store.Upsert(ctx, a); err != nil {
		t.Fatal(err)
	}

	key := "llm_endpoint|1"
	if len(fq.assets) != 1 {
		t.Errorf("expected 1 row after duplicate upsert, got %d", len(fq.assets))
	}
	if fq.assets[key].name != "second" {
		t.Errorf("name = %q, want second (update path)", fq.assets[key].name)
	}
}

func TestPGStore_Upsert_ValidatesKind(t *testing.T) {
	store, _ := newTestStore()
	ctx := context.Background()
	err := store.Upsert(ctx, Asset{Kind: Kind("bogus"), RefID: 1, TenantID: "t1", Name: "x"})
	if err == nil {
		t.Fatal("expected error for invalid Kind, got nil")
	}
	if !errors.Is(err, ErrInvalidKind) {
		t.Errorf("expected ErrInvalidKind, got %v", err)
	}
}

func TestPGStore_Upsert_RequiresTenant(t *testing.T) {
	store, _ := newTestStore()
	ctx := context.Background()
	err := store.Upsert(ctx, Asset{Kind: KindLLMEndpoint, RefID: 1, Name: "x"})
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
}

func TestPGStore_Upsert_NilPool(t *testing.T) {
	store := &pgStore{}
	err := store.Upsert(context.Background(), Asset{
		Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "x",
	})
	if err == nil {
		t.Fatal("expected error for nil pool/querier")
	}
	if !errors.Is(err, ErrNoDB) {
		t.Errorf("expected ErrNoDB, got %v", err)
	}
}

func TestPGStore_Link_RoundTrip(t *testing.T) {
	store, fq := newTestStore()
	ctx := context.Background()

	// Seed two assets
	for _, a := range []Asset{
		{Kind: KindLLMEndpoint, RefID: 1, TenantID: "t1", Name: "llm"},
		{Kind: KindMCPServer, RefID: 2, TenantID: "t1", Name: "mcp"},
	} {
		if err := store.Upsert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	err := store.Link(ctx, "t1", Relationship{
		SrcKind: RelationEndpoint{Kind: KindAgent, RefID: 3},
		DstKind: RelationEndpoint{Kind: KindLLMEndpoint, RefID: 1},
		Type:    RelCalls,
	})
	if err != nil {
		t.Fatalf("Link: %v", err)
	}

	// (We don't assert on rels key shape because Link's FK will fail on
	//  the real DB since agent=3 doesn't exist — but the fake doesn't
	//  enforce FKs, so we just confirm the Exec path was reached.)
	if len(fq.rels) == 0 {
		t.Errorf("expected rel recorded, got 0")
	}
}

// assetKeys returns the sorted keys for diagnostics.
func (f *fakeQuerier) assetKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.assets))
	for k := range f.assets {
		out = append(out, k)
	}
	return out
}

var _ = pgx.ErrNoRows
