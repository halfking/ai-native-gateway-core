package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/redis/go-redis/v9"
)

func TestSlidingWindowBatch_RoutesRegistered(t *testing.T) {
	mux := http.NewServeMux()
	m := &CredentialMonitorHandlers{h: &Handler{}}
	m.RegisterMonitorRoutes(mux, identityWrap)

	req, _ := http.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", nil)
	_, pattern := mux.Handler(req)
	if pattern == "" {
		t.Fatal("route /api/credentials/sliding-window/batch is not registered")
	}
}

func TestSlidingWindowBatch_Validation(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}

	t.Run("method not allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/credentials/sliding-window/batch", nil)
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("got %d want 405", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader("{"))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400", rec.Code)
		}
	})

	t.Run("items required when null", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"minutes":5}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("empty items ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"items":[]}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["count"] != float64(0) {
			t.Fatalf("count=%v", resp["count"])
		}
	})

	t.Run("too many items", func(t *testing.T) {
		items := make([]map[string]any, slidingWindowBatchMaxItems+1)
		for i := range items {
			items[i] = map[string]any{"credential_id": i + 1, "model": "m"}
		}
		body, _ := json.Marshal(map[string]any{"items": items})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", bytes.NewReader(body))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400 body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "at most") {
			t.Fatalf("expected at-most message, got %s", rec.Body.String())
		}
	})

	t.Run("minutes too large", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch",
			strings.NewReader(`{"minutes":2000,"items":[{"credential_id":1,"model":"m"}]}`))
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400", rec.Code)
		}
	})
}

func TestSlidingWindowBatch_PerItemErrorsWithoutDB(t *testing.T) {
	// No redis / no db: _request_logs fallback fails for valid pairs; missing
	// fields are rejected before load. Batch must still return 200 with
	// per-item error strings (not a whole-batch 500).
	m := &CredentialMonitorHandlers{h: &Handler{}}
	body := `{"minutes":5,"items":[
		{"credential_id":0,"model":"m"},
		{"credential_id":7,"model":""},
		{"credential_id":7,"model":"claude-sonnet-5"},
		{"credential_id":8,"model":"glm-5.1"}
	]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Count   int `json:"count"`
		Results []struct {
			CredentialID int            `json:"credential_id"`
			Model        string         `json:"model"`
			Stats        map[string]any `json:"stats"`
			Error        string         `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 4 || len(resp.Results) != 4 {
		t.Fatalf("count=%d results=%d", resp.Count, len(resp.Results))
	}
	if resp.Results[0].Error == "" || resp.Results[1].Error == "" {
		t.Fatalf("expected validation errors on first two items: %+v", resp.Results[:2])
	}
	for i := 2; i < 4; i++ {
		if resp.Results[i].Error == "" && resp.Results[i].Stats == nil {
			t.Fatalf("item %d: expected error or stats, got %+v", i, resp.Results[i])
		}
		// Without DB, load fails loudly per item.
		if resp.Results[i].Error == "" {
			t.Fatalf("item %d: expected load error without DB, got stats=%v", i, resp.Results[i].Stats)
		}
	}
}

func TestSlidingWindowBatch_MaxItemsBoundary(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	items := make([]map[string]any, slidingWindowBatchMaxItems)
	for i := range items {
		items[i] = map[string]any{"credential_id": i + 1, "model": fmt.Sprintf("m-%d", i)}
	}
	body, _ := json.Marshal(map[string]any{"minutes": 5, "items": items})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", bytes.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"] != float64(slidingWindowBatchMaxItems) {
		t.Fatalf("count=%v", resp["count"])
	}
}

func seedSlidingWindowRecorder(t *testing.T, count int) *CredentialMonitorHandlers {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	recorder := credentialhealth.NewRecorder(client, 2*time.Hour, 100)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < count; i++ {
		entry := credentialhealth.CallEntry{
			RequestID: fmt.Sprintf("req-%d", i),
			Timestamp: now.Add(-time.Duration(i) * time.Second).UnixMilli(),
			Success:   i%2 == 0,
			LatencyMs: 10 + i,
		}
		if err := recorder.Append(ctx, 17, "claude-sonnet-5", entry); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return &CredentialMonitorHandlers{h: &Handler{}, recorder: recorder, redisClient: client}
}

func TestSlidingWindowBatch_DefaultOmitsEntries(t *testing.T) {
	m := seedSlidingWindowRecorder(t, 10)
	body := `{"minutes":5,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			Stats   map[string]any               `json:"stats"`
			Entries []credentialhealth.CallEntry `json:"entries"`
			Error   string                       `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results=%d", len(resp.Results))
	}
	if resp.Results[0].Error != "" {
		t.Fatalf("unexpected error: %s", resp.Results[0].Error)
	}
	if resp.Results[0].Stats == nil {
		t.Fatal("expected stats")
	}
	if resp.Results[0].Entries != nil {
		t.Fatalf("default batch must omit entries, got %d", len(resp.Results[0].Entries))
	}
}

func TestSlidingWindowBatch_IncludeEntriesTruncates(t *testing.T) {
	m := seedSlidingWindowRecorder(t, 40)
	body := `{"minutes":5,"include_entries":true,"entry_limit":24,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			Stats   map[string]any               `json:"stats"`
			Entries []credentialhealth.CallEntry `json:"entries"`
			Error   string                       `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error != "" {
		t.Fatalf("unexpected result: %+v", resp.Results)
	}
	if len(resp.Results[0].Entries) != 24 {
		t.Fatalf("entries=%d want 24", len(resp.Results[0].Entries))
	}

	// Hard cap: entry_limit above MaxEntryRet is clamped (need enough seed rows).
	mCap := seedSlidingWindowRecorder(t, slidingWindowBatchLimit)
	bodyCap := `{"minutes":5,"include_entries":true,"entry_limit":99,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}`
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(bodyCap))
	mCap.handleSlidingWindowBatch(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("cap got %d want 200 body=%s", rec2.Code, rec2.Body.String())
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results[0].Entries) != slidingWindowBatchMaxEntryRet {
		t.Fatalf("hard-capped entries=%d want %d", len(resp.Results[0].Entries), slidingWindowBatchMaxEntryRet)
	}
}

func TestSlidingWindowJSONEntries_EmptyArrayWhenIncluded(t *testing.T) {
	raw, err := json.Marshal(struct {
		Entries *[]credentialhealth.CallEntry `json:"entries,omitempty"`
	}{Entries: slidingWindowJSONEntries(true, nil, 24)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"entries":[]}` {
		t.Fatalf("include empty: %s", raw)
	}

	raw, err = json.Marshal(struct {
		Entries *[]credentialhealth.CallEntry `json:"entries,omitempty"`
	}{Entries: slidingWindowJSONEntries(false, nil, 24)})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{}` {
		t.Fatalf("omit when not included: %s", raw)
	}

	entries := []credentialhealth.CallEntry{
		{RequestID: "r0"}, {RequestID: "r1"}, {RequestID: "r2"},
	}
	got := slidingWindowJSONEntries(true, entries, 2)
	if got == nil || len(*got) != 2 || (*got)[0].RequestID != "r0" {
		t.Fatalf("newest-first truncate: %+v", got)
	}
}

// 2026-10-16 audit #153: tenant isolation tests.
//
// SQL text must carry the tenant filter (the load-bearing defence: any caller
// holding a valid credential_id would otherwise read every other tenant's
// per-call detail). The two handler paths (single GET /api/credentials/sliding-
// window and POST /api/credentials/sliding-window/batch) must both thread the
// caller's tenant scope down to the SQL, AND must skip the Redis fast path
// for tenant_admin callers (Redis entries carry no tenant tag).
func TestSlidingWindowSQL_ContainsTenantFilter(t *testing.T) {
	// Both SQL builders must reference the 5th bind as the tenant scope and
	// the canonical form must be the same one the codebase uses elsewhere
	// (e.g. runModelHistory at admin/credential_monitor.go:1411/1434):
	//   ($N = '' OR tenant_id = $N)
	//
	// Empty string means "no filter" (super_admin / admin_key / unauth probe);
	// any other value is the caller's tenant_id (tenant_admin).
	for _, q := range []string{slidingWindowQuery(), slidingWindowHotQuery()} {
		if !strings.Contains(q, "($5 = '' OR tenant_id = $5)") {
			t.Fatalf("sliding-window SQL missing tenant filter:\n%s", q)
		}
	}
}

// TestSlidingWindowHotQueryArmsPositionallyAligned 钉住 2026-10-17 审计轮
// 回归：UNION ALL 按位置而非名字配对（外层 WHERE 引用的 cand 列名取自
// 首臂），两臂 SELECT 列表必须同列数、同名列序。#153 把 tenant_id 插进
// 两臂的不同位置 → varchar/bigint 解析期恒炸，245 上 ~12/s 报错 8 小时。
// 能整句验证语义的真库门（TEST_DATABASE_URL）是 opt-in、可被静默跳过，
// 这里加静态形状钉让每次 go test 都兜住。
func TestSlidingWindowHotQueryArmsPositionallyAligned(t *testing.T) {
	// SQL 注释里也出现过 "UNION ALL" 字样，只按整行匹配切臂。
	unionAllLine := regexp.MustCompile(`(?m)^\s*UNION ALL\s*$`)
	arms := unionAllLine.Split(slidingWindowHotQuery(), -1)
	if len(arms) != 2 {
		t.Fatalf("hot query 应恰有两个 UNION ALL 臂，实得 %d", len(arms))
	}
	fromLine := regexp.MustCompile(`(?m)^\s*FROM\s`)
	// 臂1 文本含外层查询，取最后一个行首 FROM（session 臂主 FROM）与其
	// 之前最后一个 SELECT；臂2 取第一个（v1 臂主 FROM 在 NOT EXISTS 子
	// 查询之前，其 SELECT 也是首个）。
	armColumns := func(arm string, last bool) []string {
		locs := fromLine.FindAllStringIndex(arm, -1)
		if len(locs) == 0 {
			t.Fatal("arm has no line-start FROM")
		}
		fromPos := locs[0][0]
		if last {
			fromPos = locs[len(locs)-1][0]
		}
		list := arm[:fromPos]
		sel := strings.Index(list, "SELECT")
		if last {
			sel = strings.LastIndex(list, "SELECT")
		}
		if sel < 0 {
			t.Fatal("arm has no SELECT")
		}
		list = list[sel+len("SELECT"):]
		var cols []string
		var cur strings.Builder
		depth := 0
		for _, r := range list {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 0 {
					cols = append(cols, cur.String())
					cur.Reset()
					continue
				}
			}
			cur.WriteRune(r)
		}
		return append(cols, cur.String())
	}
	a := armColumns(arms[0], true)
	b := armColumns(arms[1], false)
	if len(a) != len(b) {
		t.Fatalf("两臂列数不一致: %d vs %d\n臂1: %q\n臂2: %q", len(a), len(b), a, b)
	}
	// tenant_id（audit #153 引入）与 credential_id 是历史上错位过的两列；
	// 其余列位一旦换序同样会类型错位，靠真库门兜底。
	for _, name := range []string{"tenant_id", "credential_id"} {
		pos := func(cols []string) int {
			for i, c := range cols {
				if strings.Contains(c, name) {
					return i
				}
			}
			return -1
		}
		if pa, pb := pos(a), pos(b); pa != pb || pa < 0 {
			t.Fatalf("%s 两臂位置不一致或缺失: 臂1 pos=%d 臂2 pos=%d\n臂1: %q\n臂2: %q", name, pa, pb, a, b)
		}
	}
}

func TestSlidingWindowBatch_TenantAdminSkipsRedis(t *testing.T) {
	// tenant_admin caller, Redis seeded with valid entries, NO database.
	// The handler must skip the Redis fast path (entries carry no tenant
	// tag) and surface a per-item DB error rather than stats. Cross-tenant
	// probes must NOT get stats from Redis.
	m := seedSlidingWindowRecorder(t, 5)
	body := `{"minutes":5,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	req = setTestRequestContext(req, "tenant_admin", "tenant-a", "tenant-admin-user")
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			Stats   map[string]any               `json:"stats"`
			Entries []credentialhealth.CallEntry `json:"entries"`
			Error   string                       `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results=%d", len(resp.Results))
	}
	// Cross-tenant probe must NOT have been served from Redis.
	if resp.Results[0].Stats != nil {
		t.Fatalf("tenant_admin must not see Redis stats: %+v", resp.Results[0].Stats)
	}
	if resp.Results[0].Error == "" {
		t.Fatalf("tenant_admin without DB must surface per-item error, got: %+v", resp.Results[0])
	}
}

func TestSlidingWindowBatch_SuperAdminUsesRedis(t *testing.T) {
	// super_admin caller, Redis seeded with valid entries, NO database.
	// Existing behaviour preserved: super_admin still reads from Redis
	// (admin_key and super_admin keep the cross-tenant view).
	m := seedSlidingWindowRecorder(t, 5)
	body := `{"minutes":5,"items":[{"credential_id":17,"model":"claude-sonnet-5"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(body))
	req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
	m.handleSlidingWindowBatch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []struct {
			Stats   map[string]any               `json:"stats"`
			Entries []credentialhealth.CallEntry `json:"entries"`
			Error   string                       `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error != "" {
		t.Fatalf("super_admin should still get Redis stats: %+v", resp.Results)
	}
	if resp.Results[0].Stats == nil {
		t.Fatal("super_admin expected Redis-backed stats")
	}
}

func TestHandleSlidingWindow_TenantAdminSkipsRedis(t *testing.T) {
	// Same defence on the single GET endpoint: tenant_admin must NOT be
	// served from Redis. With no DB, handler returns 500 (writeInternalErr
	// for the DB fallback failure) rather than leaking Redis entries.
	m := seedSlidingWindowRecorder(t, 5)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/sliding-window?credential_id=17&model=claude-sonnet-5", nil)
	req = setTestRequestContext(req, "tenant_admin", "tenant-a", "tenant-admin-user")
	m.handleSlidingWindow(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("tenant_admin without DB must not get 200, body=%s", rec.Body.String())
	}
}

func TestHandleSlidingWindow_SuperAdminUsesRedis(t *testing.T) {
	// super_admin on the single endpoint still uses Redis fast path.
	m := seedSlidingWindowRecorder(t, 5)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/sliding-window?credential_id=17&model=claude-sonnet-5", nil)
	req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
	m.handleSlidingWindow(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Source string                       `json:"source"`
		Stats  map[string]any               `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "redis" {
		t.Fatalf("source=%q want redis", resp.Source)
	}
	if resp.Stats == nil {
		t.Fatal("super_admin expected Redis-backed stats")
	}
}
