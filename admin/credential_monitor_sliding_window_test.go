package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
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
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("got %d want 405", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader("{"))
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400", rec.Code)
		}
	})

	t.Run("items required when null", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"minutes":5}`))
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
		m.handleSlidingWindowBatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got %d want 400 body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("empty items ok", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader(`{"items":[]}`))
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
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
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
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
		req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
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
	req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
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
	req = setTestRequestContext(req, "super_admin", "", "super-admin-user")
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
	req2 = setTestRequestContext(req2, "super_admin", "", "super-admin-user")
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
//
// ★ 2026-10-20（迁移 890，v1 族真删）：hot 查询的两臂（会话臂 + v1 臂）
// 收敛为**一条**（会话族 hot + details 特征层）。
// ⇒ 「两臂位置一致」这个不变式**不再有对象**（一条臂没有「配对」可言），
// 但它背后的那个事实还在：**外层 WHERE/ORDER 引用的每个列名，必须在内层
// 投影里存在**。列名对不上 ⇒ 解析期 42703（列不存在），症状是整个端点 500。
// ⇒ 判据换成守这条等价不变式。
func TestSlidingWindowHotQueryArmsPositionallyAligned(t *testing.T) {
	q := slidingWindowHotQuery()

	// ① 恰有**一条**臂：v1 臂已随 890 退役。多一条 UNION ALL 臂 ⇒ 它读了一个
	//    已删除的族 ⇒ 42P01。
	//
	//    ⚠ 这里**不做**「有第二条臂就红」的反向断言 —— 那会把「有人把 v1
	//    臂加回来」与「有人把会话臂拆成两条合法 UNION」混为一谈。真正的
	//    负向判据在下面 ③（v1 族关系名不得在场）。
	unionAllLine := regexp.MustCompile(`(?m)^\s*UNION ALL\s*$`)
	arms := unionAllLine.Split(q, -1)
	if len(arms) != 1 {
		t.Fatalf("hot query 应恰有 1 条臂（v1 臂已随 890 退役），实得 %d:\n%s", len(arms), q)
	}

	// ② 内层投影必须覆盖外层引用到的**每一个**列名。
	//
	//    「两臂位置一致」没了，但「外层 WHERE/ORDER 引用的列在内层投影里
	//    存在」这条还在 —— 列名对不上 ⇒ 解析期 42703（列不存在）⇒ 整个端点 500。
	inner, outer := splitInnerOuterQuery(t, q)
	innerAliases := projectedAliases(innerSelectList(t, inner))
	for _, ref := range outerColumnRefs(outer) {
		if !innerAliases[ref] {
			t.Errorf("外层引用了 %q，而内层投影里没有这一列 —— 解析期 42703（列不存在）⇒ 整个端点 500。\n"+
				"内层投影别名: %v\n外层: %s", ref, sortedAliasKeys(innerAliases), strings.TrimSpace(outer))
		}
	}
	// 历史错位过的两列必须**仍然**在内层投影里（#153 把 tenant_id 插进两臂的
	// 不同位置 ⇒ varchar/bigint 解析期恒炸，245 上 ~12/s 报错 8 小时）。
	for _, must := range []string{"tenant_id", "credential_id"} {
		if !innerAliases[must] {
			t.Errorf("内层投影缺少 %q（#153 的历史错位列）—— 外层 WHERE/ORDER 引用它 ⇒ 解析期报错", must)
		}
	}

	// ③ 负向判据：v1 族关系名不得在场（按标识符边界）。
	if hits := retiredV1RelationsInSQL(q); len(hits) > 0 {
		t.Errorf("hot query 仍引用已退役 v1 族关系名 %v —— v1 族已随 890 被 DROP，读它 = 42P01：\n%s", hits, q)
	}
	// 就地阳性对照：同一把尺子必须能认出 v1 形态（否则上面那条恒绿）。
	if hits := retiredV1RelationsInSQL("FROM _request_logs_hot rl"); len(hits) == 0 {
		t.Error("retiredV1RelationsInSQL 认不出 v1 形态 —— 负向断言已恒绿，判据坏了")
	}
}

// splitInnerOuterQuery 把 `SELECT … FROM ( … ) cand <outer>` 形态的 SQL 拆成
// 内层子查询与外层部分。
func splitInnerOuterQuery(t *testing.T, q string) (inner, outer string) {
	t.Helper()
	open := strings.Index(q, "FROM (")
	if open < 0 {
		t.Fatalf("query has no FROM ( … ) subquery:\n%s", q)
	}
	depth := 0
	closeAt := -1
	for i := open + len("FROM (") - 1; i < len(q); i++ {
		switch q[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				closeAt = i
			}
		}
		if closeAt >= 0 {
			break
		}
	}
	if closeAt < 0 {
		t.Fatalf("unbalanced parentheses in query:\n%s", q)
	}
	inner = q[open+len("FROM (") : closeAt]
	outer = q[closeAt+1:]
	return inner, outer
}

// innerSelectList 取内层子查询的 **SELECT 列表**（到行首 FROM 之前为止）。
//
// ⚠ 不能把整个内层子查询当列表：内层后面还跟着 FROM/JOIN/ON，
// 而 ON 子句里的列名（d.tenant_id 之类）会被误当成投影出来的别名。
func innerSelectList(t *testing.T, inner string) string {
	t.Helper()
	sel := strings.Index(inner, "SELECT")
	if sel < 0 {
		t.Fatalf("inner subquery has no SELECT:\n%s", inner)
	}
	list := inner[sel+len("SELECT"):]
	if loc := regexp.MustCompile(`(?m)^\s*FROM\s`).FindStringIndex(list); loc != nil {
		list = list[:loc[0]]
	}
	return list
}

// projectedAliases 取一段 SELECT 列表里的**列别名**集合。
//
// 别名取「AS 之后的标识符」或「最后一个点之后的标识符」；没有别名时取表达式
// 本身的名字（`t.request_id` ⇒ request_id）。
func projectedAliases(selectList string) map[string]bool {
	out := map[string]bool{}
	for _, item := range splitTopLevelSelect(selectList) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if i := strings.LastIndex(strings.ToUpper(item), " AS "); i >= 0 {
			out[strings.TrimSpace(item[i+4:])] = true
			continue
		}
		if i := strings.LastIndex(item, "."); i >= 0 {
			out[strings.TrimSpace(item[i+1:])] = true
			continue
		}
		out[item] = true
	}
	return out
}

// splitTopLevelSelect 按顶层逗号切分（括号内的逗号不算）。
func splitTopLevelSelect(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// outerColumnRefs 取外层部分里出现的**列引用**（排除 SQL 关键字、函数名、
// 占位符与别名）。
func outerColumnRefs(outer string) []string {
	// ⚠ **先剥 SQL 注释与引号字面量**：外层那段里有中文注释
	//（「…outbound_model 与 model_offers 大小写…」）和带引号的示例值
	//（"MiniMax-M3"）⇒ 不剥就会把它们当成列引用 ⇒ 一堆假阳性。
	outer = stripSQLComments(outer)
	outer = regexp.MustCompile(`'(?:''|[^'])*'`).ReplaceAllString(outer, " ")
	skip := map[string]bool{
		"where": true, "and": true, "or": true, "not": true, "order": true, "by": true,
		"desc": true, "asc": true, "limit": true, "offset": true, "select": true,
		"from": true, "as": true, "on": true, "join": true, "left": true, "inner": true,
		"outer": true, "group": true, "having": true, "null": true, "is": true,
		"true": true, "false": true, "now": true, "interval": true, "minutes": true,
			// 2026-10-30（252SQL 审计轮）：窗口谓词从 `created_at > NOW() - ($4 * INTERVAL '1 minute')`
			// 改成 `created_at > NOW() - make_interval(mins => $4::int)`。make_interval 是
			// 函数名、mins 是它的命名参数 —— 都不是列引用。漏进 skip 表 ⇒ 外层列引用
			// 扫描把它们当成「内层投影缺失的列」⇒ 假红。
			"make_interval": true, "mins": true,
			"lower": true, "coalesce": true, "extract": true, "epoch": true, "bigint": true,
		"text": true, "int": true, "integer": true, "case": true, "when": true,
		"then": true, "else": true, "end": true, "cast": true, "cand": true,
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.FieldsFunc(outer, func(r rune) bool {
		return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		low := strings.ToLower(tok)
		// ⚠ 纯数字是 $N 占位符的残片（"$1" 被字段切分器切成 "1"），
		// 不是列名 ⇒ 必须排除，否则每条查询都会报一堆假阳性。
		if low == "" || isAllDigitsToken(low) {
			continue
		}
		if skip[low] || seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, low)
	}
	return out
}

func isAllDigitsToken(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func sortedAliasKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
		Source string         `json:"source"`
		Stats  map[string]any `json:"stats"`
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
