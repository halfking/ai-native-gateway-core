package admin

// 数据初始化（data-init）功能的单测。
//
// # 这组测试守的是哪几件事
//
// ① **客户端不能指定表名** ⇒ 类别 key 之外的输入必须被拒（buildDataInitPlan）。
// ② **删除计划的外键安全顺序** ⇒ session_summaries 必须在 tenants 之前，
//    credential_probe_configs / credential_probes 必须在 credentials 之前。
// ③ **受保护行不被删** ⇒ 系统 key / 超级管理员 / default 租户的 KEEP 谓词
//    出现在 DELETE 语句里，且谓词参数是绑定参数（不是拼进 SQL 的字面量）。
// ④ **令牌不可伪造、不可过期、不可换类别** ⇒ issue/verify 的四条负控。
// ⑤ **注册表自身不漂移** ⇒ 类别集合与全局顺序一致、表清单互不重叠。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// dataInitTestActor 测试用操作者。
func dataInitTestActor() dataInitActor {
	return dataInitActor{Username: "alice", TenantID: "acme", UserID: 7, Role: "super_admin"}
}

// planSQL 把计划渲染成可断言的文本。
func planSQL(p *dataInitPlan) string {
	var b strings.Builder
	for _, d := range p.Deletes {
		b.WriteString(d.SQL)
		b.WriteString("\n")
	}
	return b.String()
}

// planIndex 返回某张表在计划中的位置（-1 = 不存在）。
func planIndex(p *dataInitPlan, table string) int {
	for i, d := range p.Deletes {
		if d.Table == table {
			return i
		}
	}
	return -1
}

func TestBuildDataInitPlan_RejectsUnknownCategory(t *testing.T) {
	// ★ 安全属性 ①：未知类别必须报错，不能静默忽略。
	_, err := buildDataInitPlan([]string{"request_records", "pg_catalog; DROP TABLE users"}, dataInitTestActor())
	if err == nil {
		t.Fatal("expected error for unknown category, got nil")
	}
	if !strings.Contains(err.Error(), "unknown category") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildDataInitPlan_RejectsEmptySelection(t *testing.T) {
	if _, err := buildDataInitPlan(nil, dataInitTestActor()); err == nil {
		t.Fatal("expected error for empty category list")
	}
	if _, err := buildDataInitPlan([]string{"", "  "}, dataInitTestActor()); err == nil {
		t.Fatal("expected error for blank category list")
	}
}

func TestBuildDataInitPlan_DedupesCategories(t *testing.T) {
	p, err := buildDataInitPlan([]string{"users", "users", "users"}, dataInitTestActor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	n := 0
	for _, d := range p.Deletes {
		if d.Table == "users" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected users deleted once, got %d deletes", n)
	}
}

func TestBuildDataInitPlan_ForeignKeySafeOrder(t *testing.T) {
	// ★ 外键安全：session_summaries 是 tenants 的 CASCADE 子表 ⇒ 必须先删。
	// ★ credential_probe_configs / credential_probes 是 credentials 的
	//   NO ACTION 子表 ⇒ 必须先删，否则 DELETE FROM credentials 撞外键。
	p, err := buildDataInitPlan(dataInitCategoryOrder, dataInitTestActor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	summaries := planIndex(p, "session_summaries")
	tenants := planIndex(p, "tenants")
	if summaries < 0 || tenants < 0 {
		t.Fatalf("missing tables in plan: summaries=%d tenants=%d", summaries, tenants)
	}
	if summaries > tenants {
		t.Errorf("session_summaries (idx %d) must be deleted before tenants (idx %d)", summaries, tenants)
	}

	probeConfigs := planIndex(p, "credential_probe_configs")
	probes := planIndex(p, "credential_probes")
	credentials := planIndex(p, "credentials")
	if probeConfigs < 0 || probes < 0 || credentials < 0 {
		t.Fatalf("missing credential tables in plan")
	}
	if probeConfigs > credentials {
		t.Errorf("credential_probe_configs (idx %d) must precede credentials (idx %d)", probeConfigs, credentials)
	}
	if probes > credentials {
		t.Errorf("credential_probes (idx %d) must precede credentials (idx %d)", probes, credentials)
	}
}

func TestBuildDataInitPlan_KeepsProtectedRows(t *testing.T) {
	p, err := buildDataInitPlan([]string{"api_keys", "users", "tenants", "probe_records"}, dataInitTestActor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sql := planSQL(p)

	// 系统 key 保留。
	if !strings.Contains(sql, "DELETE FROM public.api_keys WHERE NOT (is_system = true)") {
		t.Errorf("api_keys keep-predicate missing; plan:\n%s", sql)
	}
	// 超级管理员 + 操作者本人保留。
	if !strings.Contains(sql, "role = 'super_admin' OR username = $1") {
		t.Errorf("users keep-predicate missing; plan:\n%s", sql)
	}
	// default 租户 + 操作者租户保留。
	if !strings.Contains(sql, "code = 'default' OR code = $1") {
		t.Errorf("tenants keep-predicate missing; plan:\n%s", sql)
	}
	// 运行数据类别整表可删（没有保留谓词）。
	if !strings.Contains(sql, "DELETE FROM public.node_probe_runs\n") {
		t.Errorf("operational table should be deleted wholesale; plan:\n%s", sql)
	}
}

func TestBuildDataInitPlan_KeepArgsAreBoundParameters(t *testing.T) {
	// ★ 谓词里的操作者身份必须是**绑定参数**，不能被拼进 SQL 文本。
	p, err := buildDataInitPlan([]string{"users"}, dataInitActor{Username: "'; DROP TABLE users; --", TenantID: "default"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sql := planSQL(p)
	if strings.Contains(sql, "DROP TABLE") {
		t.Fatalf("operator identity leaked into SQL text: %s", sql)
	}
	for _, d := range p.Deletes {
		if d.Table != "users" {
			continue
		}
		if len(d.Args) != 1 {
			t.Fatalf("expected 1 bound arg for users keep-predicate, got %d", len(d.Args))
		}
		if d.Args[0] != "'; DROP TABLE users; --" {
			t.Fatalf("unexpected bound arg: %v", d.Args[0])
		}
	}
}

func TestBuildDataInitPlan_TableNamesComeFromRegistryOnly(t *testing.T) {
	// ★ 计划里的每个表名都必须来自注册表（不可能由请求构造出来）。
	allowed := map[string]bool{}
	for _, c := range dataInitCategories {
		for _, t := range c.Tables {
			allowed[t.Name] = true
		}
	}
	p, err := buildDataInitPlan(dataInitCategoryOrder, dataInitTestActor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, d := range p.Deletes {
		if !allowed[d.Table] {
			t.Errorf("plan references table %q which is not in the registry", d.Table)
		}
		if !strings.HasPrefix(d.SQL, "DELETE FROM public."+d.Table) {
			t.Errorf("unexpected SQL shape for %q: %s", d.Table, d.SQL)
		}
	}
}

func TestDataInitCategories_OrderCoversEveryCategory(t *testing.T) {
	// ★ 全局顺序必须覆盖全部类别，否则某类别会被排在数据InitCategoryOrder 之外。
	inOrder := map[string]bool{}
	for _, k := range dataInitCategoryOrder {
		inOrder[k] = true
	}
	for _, c := range dataInitCategories {
		if !inOrder[c.Key] {
			t.Errorf("category %q missing from dataInitCategoryOrder", c.Key)
		}
	}
	if len(inOrder) != len(dataInitCategories) {
		t.Errorf("dataInitCategoryOrder has %d entries but %d categories exist", len(inOrder), len(dataInitCategories))
	}
}

func TestDataInitCategories_TableListsAreDisjoint(t *testing.T) {
	// ★ 表清单互不重叠 ⇒ 预览行数不会重复计数。
	seen := map[string]string{}
	for _, c := range dataInitCategories {
		for _, tbl := range c.Tables {
			if prev, dup := seen[tbl.Name]; dup {
				t.Errorf("table %q appears in both %q and %q", tbl.Name, prev, c.Key)
				continue
			}
			seen[tbl.Name] = c.Key
		}
	}
}

func TestDataInitCategories_EveryCategoryHasTablesAndLevel(t *testing.T) {
	for _, c := range dataInitCategories {
		if len(c.Tables) == 0 {
			t.Errorf("category %q has no tables", c.Key)
		}
		if c.LabelKey == "" {
			t.Errorf("category %q has no label key", c.Key)
		}
		switch c.Level {
		case dataInitLevelOperational, dataInitLevelConfig:
		default:
			t.Errorf("category %q has unknown level %q", c.Key, c.Level)
		}
	}
}

func TestDataInitToken_RoundTrip(t *testing.T) {
	p, err := buildDataInitPlan([]string{"request_records"}, dataInitTestActor())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, expires, err := issueDataInitToken(p, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	if expires.Before(time.Now()) {
		t.Error("token should expire in the future")
	}
	if err := verifyDataInitToken(token, p, []string{"request_records"}); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestDataInitToken_RejectsTamperedToken(t *testing.T) {
	p, _ := buildDataInitPlan([]string{"request_records"}, dataInitTestActor())
	token, _, err := issueDataInitToken(p, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	rawPart, macPart, _ := strings.Cut(token, ".")
	// 换掉载荷（保留原签名）⇒ 必须被拒。
	tampered := "00" + rawPart[2:] + "." + macPart
	if err := verifyDataInitToken(tampered, p, []string{"request_records"}); err == nil {
		t.Fatal("expected tampered token to be rejected")
	}
	// 换掉签名 ⇒ 必须被拒。
	tampered2 := rawPart + "." + "00" + macPart[2:]
	if err := verifyDataInitToken(tampered2, p, []string{"request_records"}); err == nil {
		t.Fatal("expected bad signature to be rejected")
	}
	// 形状不对 ⇒ 必须被拒。
	if err := verifyDataInitToken("garbage", p, []string{"request_records"}); err == nil {
		t.Fatal("expected malformed token to be rejected")
	}
}

func TestDataInitToken_RejectsDifferentCategorySet(t *testing.T) {
	p, _ := buildDataInitPlan([]string{"request_records"}, dataInitTestActor())
	token, _, err := issueDataInitToken(p, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	if err := verifyDataInitToken(token, p, []string{"users"}); err == nil {
		t.Fatal("expected token issued for other categories to be rejected")
	}
}

func TestDataInitToken_RejectsDifferentPlan(t *testing.T) {
	p1, _ := buildDataInitPlan([]string{"request_records"}, dataInitTestActor())
	token, _, err := issueDataInitToken(p1, []string{"request_records"})
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	// 另一类别 ⇒ 计划摘要不同 ⇒ 令牌不得通用。
	p2, _ := buildDataInitPlan([]string{"users"}, dataInitActor{Username: "bob", TenantID: "default"})
	if err := verifyDataInitToken(token, p2, []string{"users"}); err == nil {
		t.Fatal("expected token issued for a different plan to be rejected")
	}
	// 同一类别、同一操作者 ⇒ 计划摘要一致 ⇒ 通过。
	token2, _, err := issueDataInitToken(p2, []string{"users"})
	if err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	if err := verifyDataInitToken(token2, p2, []string{"users"}); err != nil {
		t.Fatalf("verify failed for identical plan: %v", err)
	}
}

func TestDataInitToken_RejectsExpiredToken(t *testing.T) {
	p, _ := buildDataInitPlan([]string{"request_records"}, dataInitTestActor())
	if _, _, err := issueDataInitToken(p, []string{"request_records"}); err != nil {
		t.Fatalf("issue failed: %v", err)
	}
	// 手工签发一个「一小时前」的令牌（用同一个密钥）。
	key, err := dataInitSigner()
	if err != nil {
		t.Fatalf("signer unavailable: %v", err)
	}
	payload := dataInitTokenPayload{
		Categories: []string{"request_records"},
		Digest:     p.digest(),
		IssuedAt:   time.Now().Add(-time.Hour).Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(raw)
	expired := hex.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil))
	if err := verifyDataInitToken(expired, p, []string{"request_records"}); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestDataInitActorFromRequest_Defaults(t *testing.T) {
	// 无 AuthContext 时：用户名回落到 requestActor，租户回落到 default。
	r := httptest.NewRequest("GET", "/api/admin/data-lifecycle/data-init/catalog", nil)
	a := dataInitActorFromRequest(r)
	if a.TenantID != "default" {
		t.Errorf("expected default tenant, got %q", a.TenantID)
	}
	if a.Username == "" {
		t.Error("expected non-empty actor username")
	}
}
