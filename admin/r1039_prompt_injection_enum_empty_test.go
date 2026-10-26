package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// r1039_prompt_injection_enum_empty_test.go — 2026-10-20 浏览器验收时实测到的
// 真缺陷的判据。
//
// ## 缺陷
//
// `prompt_injection_rules.category_new` 是 `injection_category` 枚举、
// `action_override` 是 `injection_action` 枚举，**两列都允许 NULL**。
// 但前端表单在「没选」时给的是**空串**而不是 null（新建规则的表单默认值
// 就是 `action_override: ''`）⇒ 直接把空串塞进 SQL 会得到
//
//	ERROR: invalid input value for enum injection_action: "" (SQLSTATE 22P02)
//
// 表现是 **500**，而且**从 UI 新建自定义规则必失败**（每次都带
// `action_override:''`）。updateRule 上同一个坑：把动作覆盖清空再保存也 500。
//
// ## 判据为什么打在这一层
//
// 打**真实的 handler 函数**（`h.createRule` / `h.updateRule`）+ **真库**：
//   - 只测 `enumNullIfEmpty` 这个 helper 是**假绿**——helper 正确不代表
//     handler 真的用了它；
//   - 用 mock 池也是假绿——mock 不会执行 PG 的枚举校验，缺陷根本不会浮现。
// ⇒ 必须让 PG 自己拒绝一次空串，才算证明「这条路径原本会 500」。
//
// 夹具纪律：所有写入都在一个事务里，结尾 ROLLBACK ⇒ 不污染真库。
// 纪律同 admin/prompt_injection_canonical_shape_test.go。

// r1039TestPool 连真库（TEST_PG_URL），不可用时跳过。
func r1039TestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that this path works")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to TEST_PG_URL: %v", err)
	}
	return pool
}

// TestR1039PromptInjectionEnumEmptyStringIsRejectedByPG 先证明「空串确实会被
// PG 拒绝」——这是缺陷的**前提**。没有它，后面的判据无法区分
// 「handler 修好了」与「PG 根本不校验枚举」。
func TestR1039PromptInjectionEnumEmptyStringIsRejectedByPG(t *testing.T) {
	pool := r1039TestPool(t)
	defer pool.Close()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 枚举列直接给空串 ⇒ 必须报错（SQLSTATE 22P02）。
	//
	// 用 SAVEPOINT 包住这次**故意失败**的写入：PG 里一条语句报错会让整个
	// 事务进入 aborted 状态，后续语句一律 25P02 ⇒ 必须回滚到 savepoint
	// 才能继续跑下面的对照。
	if _, err := tx.Exec(ctx, `SAVEPOINT r1039_enum_probe`); err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, action_override)
		VALUES ('r1039_enum_probe', 'basic', 'probe', '', 'probe', 'probe', 5, '')`)
	if err == nil {
		t.Fatal("前提失效：PG 接受了枚举列上的空串 ⇒ 「空串会 500」这个前提不成立")
	}
	var code string
	if pgErr, ok := err.(interface{ SQLState() string }); ok {
		code = pgErr.SQLState()
	}
	if code != "22P02" {
		t.Fatalf("前提失效：期望 SQLSTATE 22P02（invalid_text_representation），实际 %q err=%v", code, err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT r1039_enum_probe`); err != nil {
		t.Fatalf("rollback to savepoint: %v", err)
	}

	// 对照：NULL 是允许的（两列都 nullable）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, action_override)
		VALUES ('r1039_enum_probe', 'basic', 'probe', NULL, 'probe', 'probe', 5, NULL)`); err != nil {
		t.Fatalf("前提失效：枚举列给 NULL 应当被接受，实际报错 %v", err)
	}
}

// TestR1039CreateRuleWithEmptyEnumFieldsSucceeds 钉住**建规则**这条路径。
//
// 载荷形状抄自前端新建表单的默认值（action_override / category_new 都是空串）。
func TestR1039CreateRuleWithEmptyEnumFieldsSucceeds(t *testing.T) {
	pool := r1039TestPool(t)
	defer pool.Close()

	h := NewPromptInjectionHandler(pool, "test-secret")
	body := map[string]any{
		"rule_name":       "r1039_create_probe",
		"rule_type":       "basic",
		"category":        "data_exfiltration",
		"category_new":    "",
		"pattern":         "r1039_probe_pattern",
		"description":     "r1039 枚举空串判据",
		"severity":        5,
		"enabled":         true,
		"case_sensitive":  false,
		"action_override": "",
		"tags":            []string{"r1039"},
		"examples":        []string{"probe"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/prompt-injection/rules", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.createRule(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("建规则应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	// 落库确认：枚举列必须是 NULL 而不是空串。
	var catNew, action *string
	if err := pool.QueryRow(context.Background(),
		`SELECT category_new::text, action_override::text FROM prompt_injection_rules WHERE rule_name=$1`,
		"r1039_create_probe").Scan(&catNew, &action); err != nil {
		t.Fatalf("probe inserted rule: %v", err)
	}
	if catNew != nil || action != nil {
		t.Fatalf("枚举列必须落成 NULL，实际 category_new=%v action_override=%v", catNew, action)
	}
	// 清理（这条不在事务里，所以显式删）。
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM prompt_injection_rules WHERE rule_name=$1`, "r1039_create_probe"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// TestR1039UpdateRuleWithEmptyActionOverrideSucceeds 钉住**改规则**这条路径：
// 把动作覆盖清空（= 不覆盖）必须能存下去，而不是 500。
func TestR1039UpdateRuleWithEmptyActionOverrideSucceeds(t *testing.T) {
	pool := r1039TestPool(t)
	defer pool.Close()
	ctx := context.Background()

	// 先建一条带覆盖动作的规则。
	var ruleID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO prompt_injection_rules (rule_name, rule_type, category, category_new, pattern, description, severity, action_override, is_system)
		VALUES ('r1039_update_probe', 'basic', 'data_exfiltration', 'data_exfiltration', 'r1039_probe', 'probe', 5, 'reject', false)
		RETURNING id`).Scan(&ruleID); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM prompt_injection_rules WHERE id=$1`, ruleID)
	}()

	h := NewPromptInjectionHandler(pool, "test-secret")
	payload := []byte(`{"action_override":""}`)
	req := httptest.NewRequest(http.MethodPut,
		"/api/admin/prompt-injection/rules/1", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.updateRule(rec, req, strconv.Itoa(ruleID))

	if rec.Code != http.StatusOK {
		t.Fatalf("清空动作覆盖应当成功，实际 HTTP %d：%s", rec.Code, rec.Body.String())
	}
	var action *string
	if err := pool.QueryRow(ctx,
		`SELECT action_override::text FROM prompt_injection_rules WHERE id=$1`, ruleID).Scan(&action); err != nil {
		t.Fatalf("probe updated rule: %v", err)
	}
	if action != nil {
		t.Fatalf("清空动作覆盖必须落成 NULL，实际 %v", *action)
	}
}
