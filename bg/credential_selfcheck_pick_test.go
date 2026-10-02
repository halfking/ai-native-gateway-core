package bg

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/recentmodels"
	"github.com/pashagolub/pgxmock/v4"
)

func newSelfcheckMock(t *testing.T) (*CredentialSelfcheckWorker, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	return &CredentialSelfcheckWorker{db: mock}, mock
}

var scErrNoRows = pgx.ErrNoRows

func TestSelectSelfcheckPrimaryPrefersHighestRecentUsage(t *testing.T) {
	bindings := []selfcheckBinding{
		{raw: "featured-low", standardized: "featured-low"},
		{raw: "popular-high", standardized: "popular-high"},
	}
	model, strategy := selectSelfcheckPrimary(bindings, []string{"featured-low"}, []recentmodels.Entry{
		{Model: "featured-low", Count: 5},
		{Model: "popular-high", Count: 10},
	})
	if model != "popular-high" || strategy != "recent" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want popular-high/recent", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryUsesFeaturedTieBreak(t *testing.T) {
	bindings := []selfcheckBinding{
		{raw: "featured-model", standardized: "featured-model"},
		{raw: "same-score", standardized: "same-score"},
	}
	model, strategy := selectSelfcheckPrimary(bindings, []string{"featured-model"}, []recentmodels.Entry{
		{Model: "featured-model", Count: 8},
		{Model: "same-score", Count: 8},
	})
	if model != "featured-model" || strategy != "featured" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want featured-model/featured", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryMatchesStandardizedName(t *testing.T) {
	model, strategy := selectSelfcheckPrimary(
		[]selfcheckBinding{{raw: "vendor/gpt-4o", standardized: "gpt-4o"}},
		[]string{"gpt-4o"}, nil,
	)
	if model != "vendor/gpt-4o" || strategy != "featured" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want raw binding matched by standard name", model, strategy)
	}
}

func TestSelectSelfcheckPrimaryNeverChoosesUnrankedModel(t *testing.T) {
	model, strategy := selectSelfcheckPrimary(
		[]selfcheckBinding{{raw: "obscure-model", standardized: "obscure-model"}}, nil, nil,
	)
	if model != "" || strategy != "" {
		t.Fatalf("selectSelfcheckPrimary()=(%q,%q), want no random fallback", model, strategy)
	}
}

func TestCredentialSelfcheckWindowIsFifteenMinutes(t *testing.T) {
	if credentialSelfcheckWindow != 15*time.Minute {
		t.Fatalf("self-check window = %v, want 15m so failed credentials can recover the same day", credentialSelfcheckWindow)
	}
}

func TestPickDueCredentialFiltersByCredentialRun(t *testing.T) {
	src, err := os.ReadFile("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "scr.model_name = 'cred-' || c.id::text") {
		t.Fatal("per-credential self-check watermark filter missing")
	}
}

// With a 15m window and LIMIT 1 per 5m tick, ordering by the newest error
// first lets 3 noisy credentials starve everyone else. Least-recently
// checked must win so every erroring credential rotates through.
//
// 2026-10-02（§9.26）：这条断言原来钉的是 **ORDER BY 那一行的完整字面量**
// （`... ASC, e.last_error_at DESC, c.id` 单行）。补 session 臂时把 ORDER BY
// 拆成两行、并把错误键换成 COALESCE(e, se)，它就红了 ——
// **一个钉字面量的守卫会把纯排版变化报成缺陷，同时对「语义退化」不敏感。**
// 改为按**排序键的先后次序**判定，与排版解耦：
// 主键必须是 l.last_at（最久未检优先），错误键在其后，c.id 最后。
func TestPickDueCredentialRotatesLeastRecentlyChecked(t *testing.T) {
	orderBy := orderByClause(selfcheckPickSQL(t))
	if orderBy == "" {
		t.Fatal("pickDueCredential 里找不到 ORDER BY 子句")
	}
	// 主键（最久未检优先）——这是本测试真正要守的语义。
	lead := strings.TrimSpace(orderBy[len("ORDER BY"):])
	if !strings.HasPrefix(lead, "COALESCE(l.last_at,") || !strings.Contains(lead, "ASC") {
		t.Errorf("ORDER BY 的主键必须是最久未检优先（COALESCE(l.last_at, …) ASC），\n"+
			"否则 3 个吵闹的凭据会把其余全部饿死。当前 ORDER BY：%s", orderBy)
	}
	// 错误键必须在主键之后；它的表达式形态由 §9.26 的门单独守。
	idxL, idxErr := strings.Index(orderBy, "l.last_at"), strings.Index(orderBy, "last_error_at")
	if idxErr < 0 {
		t.Errorf("ORDER BY 里没有错误时间键，轮转语义退化了。当前 ORDER BY：%s", orderBy)
	} else if idxErr < idxL {
		t.Errorf("错误时间键排在了最久未检之前 —— 轮转语义被反转。当前 ORDER BY：%s", orderBy)
	}
	// 确定性收尾键必须保留：没有它，并列时返回哪一行不确定。
	if !strings.Contains(orderBy, "c.id") {
		t.Errorf("ORDER BY 缺少 c.id 收尾键，并列时结果不确定。当前 ORDER BY：%s", orderBy)
	}
}

// ---- 以下为 pickDueCredential SQL 的提取辅助（§9.26 建）----
//
// 放在这个文件而不是 S4 门文件里：pickDueCredential 的**行为**由本文件守，
// S4 门只是其中一组退化形状的专项守卫。共用一份提取逻辑，避免两处各写一遍
// 而漂移。

func mustReadSourceBg(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// selfcheckPickSQL returns the SQL literal passed to QueryRow inside
// pickDueCredential, with SQL line comments stripped.
func selfcheckPickSQL(t *testing.T) string {
	t.Helper()
	full := mustReadSourceBg(t, "credential_selfcheck.go")
	const start = "func (w *CredentialSelfcheckWorker) pickDueCredential("
	i := strings.Index(full, start)
	if i < 0 {
		t.Fatal("pickDueCredential not found in credential_selfcheck.go")
	}
	rest := full[i+len(start):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 {
		rest = rest[:j]
	}
	open := strings.Index(rest, "`")
	if open < 0 {
		t.Fatal("pickDueCredential 里没有找到 SQL 字面量（反引号）")
	}
	closeIdx := strings.Index(rest[open+1:], "`")
	if closeIdx < 0 {
		t.Fatal("SQL 字面量未闭合")
	}
	return stripSQLLineComments(rest[open+1 : open+1+closeIdx])
}

// orderByClause returns the ORDER BY ... up to (but excluding) LIMIT.
func orderByClause(sql string) string {
	i := strings.Index(sql, "ORDER BY")
	if i < 0 {
		return ""
	}
	rest := sql[i:]
	if j := strings.Index(rest, "LIMIT"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// stripSQLLineComments removes `--` comments while respecting string literals
// and NOT treating `--` inside a quoted string as a comment start. The SQL in
// this file contains a `'cred-' || c.id::text` literal and interval strings, so
// a naive regex would corrupt them.
func stripSQLLineComments(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		var b strings.Builder
		inString := false
		for i := 0; i < len(line); i++ {
			ch := line[i]
			switch {
			case inString:
				if ch == '\'' {
					inString = false
				}
			case ch == '\'':
				inString = true
			case ch == '-' && i+1 < len(line) && line[i+1] == '-':
				i = len(line) // consume rest of line
				continue
			}
			b.WriteByte(ch)
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

func TestSelfcheckUsesSharedRecentModelSource(t *testing.T) {
	src, err := os.ReadFile("credential_selfcheck.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"recentmodels.Read",
		// 2026-09-20 探测量策略：SQL 回退窗口从 7 天对齐到 3 天使用范围
		"interval '3 days'",
		// R50：双臂探测排除（quality_flags + origin_stage）——单臂旧拼写
		// 已被 TestProbeExclusionPredicateCallSitesR50 一并禁用。
		`fmt.Sprintf(probeTrafficExclusionPredicate, "rl", "rl")`,
		"unavailable_recover_at <= now()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("self-check picker missing %q", want)
		}
	}
	if strings.Contains(body, "credential_most_used_model") || strings.Contains(body, "rng.Shuffle") {
		t.Fatal("self-check must not use the old 24h or random model fallback")
	}
}

func TestCredentialSelfcheckDefaultsToLoopbackGateway(t *testing.T) {
	t.Setenv("LLM_GATEWAY_SELF_CHECK_BASE_URL", "")
	w := NewCredentialSelfcheckWorker(nil, "", "")
	if w.baseURL != "http://127.0.0.1:8781/v1" {
		t.Fatalf("base URL = %q", w.baseURL)
	}
}
