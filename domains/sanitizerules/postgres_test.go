package sanitizerules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/kaixuan/llm-gateway-go/internal/testguard"
)

func TestPostgresDoesNotRunDDLAndSanitizesErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewPostgresStore(db)
	mock.ExpectQuery("SELECT").WillReturnError(errors.New("password=raw-secret"))
	_, err = s.Current(context.Background())
	if err == nil || strings.Contains(err.Error(), "raw-secret") {
		t.Fatal("unsafe failure", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func integrationStore(t *testing.T) *PostgresStore {
	return integrationStoreMode(t, pgx.QueryExecModeSimpleProtocol)
}
func integrationStoreMode(t *testing.T, mode pgx.QueryExecMode) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("SANITIZE_RULES_TEST_DSN")
	if dsn == "" {
		t.Skip("SANITIZE_RULES_TEST_DSN unset; isolated PostgreSQL required")
	}
	// ★★ R92 轮（守卫线 · 续六）：下面那段 DROP 打的是**产品表**
	//   （252 生产只读核实：public.sanitize_rule_instances / sanitize_rule_events /
	//   sanitize_rule_head / sanitize_rule_versions 四张表**都存在**，relkind='r'）。
	//   ⇒ 原来只有一句注释保护（"This opt-in DSN MUST point to a disposable
	//   test database"）⇒ ★ **注释不是守卫** ⇒⇒ 一旦有人手工把这个 DSN 指向
	//   产品库，这条腿就会**真删四张产品表**。
	//   ⇒ 补真正的守卫：目标不是隔离库 ⇒ 这条腿不跑。
	//   ⇒ 零行为变化：本测试读的那个 DSN 名**不在**事实源导出名单里
	//   （事实源导出的是**同前缀的 BROWSER 版** DSN，给 admin 的浏览器夹具用）
	//   ⇒⇒ 默认本来就 skip ⇒⇒ 本处是**纯防御**。
	//
	// ★★ 注释里**刻意不写出任何事实源变量名**（本仓有一道「在范围」判据是
	//   文件级 grep 事实源变量名 ⇒ 注释里写出来会把**整个文件**拉进受测范围
	//   ⇒⇒ 分母被无意义地顶大）。⇒ 详见 handoff §19.20.47 ⑩。
	testguard.RequireIsolatedTarget(t, dsn)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultQueryExecMode = mode
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	// This opt-in DSN MUST point to a disposable test database.
	if _, err = db.Exec(`DROP TABLE IF EXISTS sanitize_rule_instances,sanitize_rule_events,sanitize_rule_head,sanitize_rule_versions CASCADE; DROP FUNCTION IF EXISTS sanitize_rule_immutable_version()`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(SchemaSQL); err != nil {
		t.Fatal(err)
	}
	return NewPostgresStore(db)
}
func TestPostgresJSONVersionsAcrossQueryModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode pgx.QueryExecMode
	}{{"production_simple", pgx.QueryExecModeSimpleProtocol}, {"extended", pgx.QueryExecModeCacheStatement}} {
		t.Run(tc.name, func(t *testing.T) {
			s := integrationStoreMode(t, tc.mode)
			cfg := testConfig()
			cfg.Rules[0].Name = "规则 O'Reilly \\ JSON 中文"
			m, err := NewManager(s, cfg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err = m.Refresh(ctx); err != nil {
				t.Fatal("baseline publication:", err)
			}
			initial := m.Current().Version
			if initial < 1 {
				t.Fatal("baseline not persisted")
			}
			next, err := s.Publish(ctx, initial, cfg, "admin", "query mode round trip", 0)
			if err != nil {
				t.Fatal(err)
			}
			rolled, err := m.Rollback(ctx, next.ID, initial, "admin", "restore baseline")
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.Current(ctx)
			if err != nil || current.ID != rolled.ID || current.RollbackOf != initial {
				t.Fatal("rollback version:", current, err)
			}
			want, _ := json.Marshal(cfg)
			got, _ := json.Marshal(current.Config)
			if string(want) != string(got) {
				t.Fatal("JSON policy changed across persistence")
			}
		})
	}
}
func TestPostgresIntegrationVersionEventsAndManagers(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := s.Publish(ctx, 0, testConfig(), "admin", "initial", 0)
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("publish: %v", err)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("CAS winners", winners.Load())
	}
	v, _ := s.Current(ctx)
	if v.ID <= 0 {
		t.Fatal("missing published version")
	}
	e := Event{TenantID: "tenant-a", RuleID: "secret", Version: v.ID, Phase: "input", Action: "block", SampleMasked: "password=raw-secret"}
	if err := s.RecordEvent(ctx, e, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(ctx, e, 100); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListEvents(ctx, EventFilter{TenantID: "tenant-a"})
	if err != nil || len(events) != 1 || events[0].Count != 2 || strings.Contains(events[0].SampleMasked, "raw-secret") {
		t.Fatal(events, err)
	}
	_, err = s.ReviewEvent(ctx, "tenant-b", events[0].ID, "false_positive", "reviewer", "scope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant review accepted", err)
	}
	if _, err = s.ReviewEvent(ctx, "tenant-a", events[0].ID, "false_positive", "reviewer", "scoped fix"); err != nil {
		t.Fatal(err)
	}
	linked, err := s.Publish(ctx, v.ID, testConfig(), "admin", "review fix", 0, ReviewLink{ID: events[0].ID, TenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	events, _ = s.ListEvents(ctx, EventFilter{TenantID: "tenant-a"})
	if events[0].ResolvedVersion != linked.ID {
		t.Fatal("missing transactional review link")
	}
	_, err = s.Publish(ctx, linked.ID, testConfig(), "admin", "wrong tenant", 0, ReviewLink{ID: events[0].ID, TenantID: "tenant-b"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("cross tenant publish accepted", err)
	}
	head, _ := s.Current(ctx)
	if head.ID != linked.ID {
		t.Fatal("failed review link advanced head")
	}
	if _, err = s.db.Exec(`UPDATE sanitize_rule_versions SET actor='tampered' WHERE id=$1`, v.ID); err == nil {
		t.Fatal("immutable history update accepted")
	}
	a, _ := NewManager(s, testConfig(), Options{InstanceID: "a"})
	b, _ := NewManager(s, testConfig(), Options{InstanceID: "b", PollInterval: func() time.Duration { return 10 * time.Millisecond }})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { b.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	restored, err := a.Rollback(ctx, linked.ID, v.ID, "admin", "restore")
	if err != nil || restored.ID <= linked.ID || restored.RollbackOf != v.ID {
		t.Fatal(restored, err)
	}
	waitFor(t, func() bool { return b.Current().Version == restored.ID })
	instances, _ := s.ListInstances(ctx)
	if len(instances) != 2 {
		t.Fatal("missing instance ACKs", instances)
	}
}

func TestPostgresSchemaReplayRollbackAndReviewTransaction(t *testing.T) {
	s := integrationStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, SchemaSQL); err != nil {
		t.Fatal("schema replay", err)
	}
	v, err := s.Publish(ctx, 0, testConfig(), "test", "baseline", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err = s.RecordEvent(ctx, Event{TenantID: "a", RuleID: id, Version: v.ID, Phase: "input", Action: "mask", Type: "secret", Field: "credential_pair"}, 100); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.ListEvents(ctx, EventFilter{})
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	for _, e := range events {
		if _, err = s.ReviewEvent(ctx, "a", e.ID, "false_positive", "test", "checked"); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Publish(ctx, v.ID, testConfig(), "test", "must rollback all links", 0, ReviewLink{ID: events[0].ID, TenantID: "a"}, ReviewLink{ID: events[1].ID, TenantID: "wrong"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatal("expected second link failure", err)
	}
	e, err := s.GetEvent(ctx, events[0].ID)
	if err != nil || e.ResolvedVersion != 0 {
		t.Fatal("partial review transaction leaked", e, err)
	}
	versions, _ := s.ListVersions(ctx, 100)
	if len(versions) != 1 {
		t.Fatal("failed transaction left history", versions)
	}
	if _, err = s.db.ExecContext(ctx, RollbackSchemaSQL); err != nil {
		t.Fatal("schema rollback", err)
	}
	if _, err = s.db.ExecContext(ctx, SchemaSQL); err != nil {
		t.Fatal("reinstall after rollback", err)
	}
}
