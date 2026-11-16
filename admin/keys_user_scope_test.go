package admin

// keys_user_scope_test.go — 2026-11-17「用户详情 API 密钥管理」审计轮的后端钉测。
//
// 本轮修掉的三个后端缺陷（详见 docs/audit/2026-11-17-用户详情API密钥管理-审计轮.md）：
//
//	B1  createKey 把 owner_user 指针（nil ⇒ NULL）直接写库，而代码算出的
//	    默认值 'admin' 只用于 getOrCreateApplicationForTenant ⇒ 不传 owner_user
//	    的密钥落库成 owner_user=NULL ⇒ 在「用户明细 → API 密钥」里完全隐身。
//	B2  listKeys 没有 owner 过滤口 ⇒ 「某用户名下密钥」只能把全站密钥拉回
//	    浏览器再前端筛（越权下发 + 全表下载）。
//	B3  setKeyEnabled(enabled=true) 对 status='revoked' 的密钥是**静默失败**：
//	    请求路径的鉴权谓词是 enabled=TRUE AND status NOT IN ('revoked','disabled')，
//	    而该函数只写 enabled、不写 status ⇒ 密钥仍然不可用，前端却收到 200。
//
// 测试分两层：
//   - 契约层（不依赖 DB）：源码级断言，钉住「不能退回旧写法」。
//   - 真库层（TEST_DATABASE_URL 门控）：真跑 createKey / listKeys / setKeyEnabled。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── 契约层 ────────────────────────────────────────────────────────

// mustReadAuditAdminSource 读取 admin 包内的源文件（契约层专用）。
func mustReadAuditAdminSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestListKeysHasOwnerFilter(t *testing.T) {
	src := mustReadAuditAdminSource(t, "keys.go")
	if !strings.Contains(src, "AND ($3 = '' OR ak.owner_user = $3)") {
		t.Fatal("listKeys must filter by owner server-side (2026-11-17 audit B2): " +
			"without it the client has to download every key in the platform")
	}
	if !strings.Contains(src, "ownerFilter := queryString(r, \"owner\")") {
		t.Fatal("listKeys must read the owner query parameter")
	}
	// created_at 是「查看 key 完整信息」的一部分（签发时间）。
	if !strings.Contains(src, "ak.tenant_id, ak.key_alias, ak.created_at") {
		t.Fatal("listKeys must return created_at so the UI can show the issuance time")
	}
}

func TestCreateKeyPersistsResolvedOwner(t *testing.T) {
	src := mustReadAuditAdminSource(t, "keys.go")
	// INSERT 必须写已解析后的 ownerUser（字符串），不能写 req.OwnerUser（指针）。
	if strings.Contains(src, "keyCiphertext, req.OwnerUser,") {
		t.Fatal("createKey must insert the resolved ownerUser, not the req.OwnerUser pointer: " +
			"a nil pointer becomes SQL NULL and the key becomes invisible to owner-scoped reads")
	}
	if !strings.Contains(src, "keyCiphertext, ownerUser,") {
		t.Fatal("createKey must insert the resolved ownerUser value")
	}
}

func TestSetKeyEnabledRejectsRevoked(t *testing.T) {
	src := mustReadAuditAdminSource(t, "keys.go")
	if !strings.Contains(src, `status == "revoked"`) {
		t.Fatal("setKeyEnabled must refuse to enable a revoked key " +
			"(2026-11-17 audit B3): enabling it only flips enabled while status stays 'revoked', " +
			"so the key keeps failing auth while the UI reports success")
	}
	if !strings.Contains(src, "cannot enable a revoked key") {
		t.Fatal("setKeyEnabled must explain why a revoked key cannot be re-enabled")
	}
}

// ── 真库层 ────────────────────────────────────────────────────────

type auditKeyRow struct {
	ID        int     `json:"id"`
	Prefix    string  `json:"key_prefix"`
	OwnerUser *string `json:"owner_user"`
	Status    string  `json:"status"`
	Enabled   bool    `json:"enabled"`
	CreatedAt *string `json:"created_at"`
}

func setupAuditKeyDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedAuditKeys 建一个应用 + 两把密钥（owner 分别是 alice / bob）。
func seedAuditKeys(t *testing.T, pool *pgxpool.Pool, code string) (appID int, aliceID int, bobID int) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO applications (code, display_name) VALUES ($1, 'audit user scope') RETURNING id`,
		code).Scan(&appID); err != nil {
		t.Fatalf("seed application: %v", err)
	}
	insert := func(hash, prefix, owner string) int {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, owner_user, enabled, status)
			VALUES ($1, 'default', $2, $3, $4, TRUE, 'active') RETURNING id`,
			appID, hash, prefix, owner).Scan(&id); err != nil {
			t.Fatalf("seed api_key %s: %v", owner, err)
		}
		return id
	}
	aliceID = insert("zz-audit-alice-"+code, "zz-alice-"+code, "alice")
	bobID = insert("zz-audit-bob-"+code, "zz-bob-"+code, "bob")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM api_keys WHERE application_id = $1`, appID)
		_, _ = pool.Exec(ctx, `DELETE FROM applications WHERE id = $1`, appID)
	})
	return appID, aliceID, bobID
}

// B2 真库验证：listKeys 带 owner 参数时只返回该 owner 的密钥。
func TestListKeysOwnerFilterRealDB(t *testing.T) {
	pool := setupAuditKeyDB(t)
	_, aliceID, bobID := seedAuditKeys(t, pool, "zz-audit-owner-"+strings.ReplaceAll(t.Name(), "/", "-"))

	h := &Handler{db: pool}
	req := httptest.NewRequest(http.MethodGet, "/api/keys?owner=alice", nil)
	req = setTestRequestContext(req, "super_admin", "default", "root")
	w := httptest.NewRecorder()
	h.listKeys(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("listKeys status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var rows []auditKeyRow
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode listKeys body: %v", err)
	}
	foundAlice, foundBob := false, false
	for _, r := range rows {
		if r.ID == aliceID {
			foundAlice = true
		}
		if r.ID == bobID {
			foundBob = true
		}
	}
	if !foundAlice {
		t.Fatalf("owner=alice must return alice's key (id=%d)", aliceID)
	}
	if foundBob {
		t.Fatalf("owner=alice must NOT return bob's key (id=%d) — the owner filter is not applied", bobID)
	}
}

// B3 真库验证：对已吊销的密钥执行 enable 必须 409（而不是假装成功的 200）。
func TestSetKeyEnabledRevokedReturnsConflictRealDB(t *testing.T) {
	pool := setupAuditKeyDB(t)
	_, aliceID, _ := seedAuditKeys(t, pool, "zz-audit-revoked-"+strings.ReplaceAll(t.Name(), "/", "-"))

	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE api_keys SET status = 'revoked', enabled = FALSE WHERE id = $1`, aliceID); err != nil {
		t.Fatalf("mark key revoked: %v", err)
	}

	h := &Handler{db: pool}
	req := httptest.NewRequest(http.MethodPatch, "/api/keys/1/enable", nil)
	req = setTestRequestContext(req, "super_admin", "default", "root")
	w := httptest.NewRecorder()
	h.setKeyEnabled(w, req, aliceID, true)

	if w.Code != http.StatusConflict {
		t.Fatalf("enabling a revoked key = %d, want 409 (body=%s); "+
			"200 would be a silent failure: enabled flips but status stays 'revoked' so auth still rejects",
			w.Code, w.Body.String())
	}
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM api_keys WHERE id = $1`, aliceID).Scan(&enabled); err != nil {
		t.Fatalf("re-read key: %v", err)
	}
	if enabled {
		t.Fatal("a rejected enable must not leave enabled=TRUE behind")
	}
}

// B1 真库验证：createKey 不传 owner_user 时，落库 owner_user 必须是解析后的
// 'admin'，而不是 NULL（NULL ⇒ 该密钥在用户名维度完全隐身）。
func TestCreateKeyOwnerDefaultsToAdminNotNullRealDB(t *testing.T) {
	pool := setupAuditKeyDB(t)
	code := "zz-audit-owner-default-" + strings.ReplaceAll(t.Name(), "/", "-")

	h := &Handler{db: pool, secret: "audit-test-secret"}
	req := httptest.NewRequest(http.MethodPost, "/api/keys",
		strings.NewReader(`{"application_code":"`+code+`"}`))
	req = setTestRequestContext(req, "super_admin", "default", "root")
	w := httptest.NewRecorder()
	h.createKey(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("createKey status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode createKey body: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM api_keys WHERE id = $1`, resp.ID)
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM applications WHERE code = $1`, code)
	})

	ctx := context.Background()
	var owner *string
	if err := pool.QueryRow(ctx, `SELECT owner_user FROM api_keys WHERE id = $1`, resp.ID).Scan(&owner); err != nil {
		t.Fatalf("read back owner_user: %v", err)
	}
	if owner == nil {
		t.Fatal("owner_user must NOT be NULL when the caller omits it " +
			"(NULL keys are invisible to every owner-scoped read)")
	}
	if *owner != "admin" {
		t.Fatalf("owner_user = %q, want %q", *owner, "admin")
	}
}
