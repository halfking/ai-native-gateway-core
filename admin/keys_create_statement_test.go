package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
)

// ★ 2026-11-23（211 建 key 全量 500 回归根修）：createKey 落库面的钉测。
//
// ★ 事故：83e3c5694 给建 key 的 INSERT 无条件加上 team_id 列，而加这一列的迁移 057
//   从未在 211 / 252 / 新装环境应用 ⇒ `column "team_id" of relation "api_keys" does not
//   exist (SQLSTATE 42703)` ⇒ 211 自 build_seq 3240 起 POST /api/keys 全量 500。
// ★ 为什么既有测试没拦住：此前的建 key 测试都停在 DB 面之前（h.db 是具体的
//   *pgxpool.Pool，nil db 进不了落库那一步）。这里按 teamGuardDB 的同款范式走窄注入面
//   keyCreateDB，让 pgxmock 按**语句形状**钉住落库语句：
//     ① 个人 key（请求不带 team_id）：语句里不得出现 team_id 列；
//     ② 团队 key：语句必须写 team_id（第 11 个参数）。
//
// 判据写法：pgxmock 默认把期望式当**正则**去匹配空白归一后的语句。期望式里
// 「key_alias\) VALUES」后面紧跟的就是参数清单 ⇒ 带 team_id 的 13 列语句对不上 ① 的
// 期望 ⇒ pgxmock 报「未预期的调用」⇒ createKey 回 500 ⇒ 本测试红——与生产失败的
// 形状一一对应。

// 个人 key 的落库语句形状（pgxmock 把空白归一成单空格）。
const personalKeyInsertPattern = `INSERT INTO api_keys \(application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark, key_alias\) VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, TRUE, \$7, \$8, 'active', \$9, \$10\) RETURNING id`

// 团队 key 的落库语句形状。
const teamKeyInsertPattern = `INSERT INTO api_keys \(application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark, key_alias, team_id\) VALUES \(\$1, \$2, \$3, \$4, \$5, \$6, TRUE, \$7, \$8, 'active', \$9, \$10, \$11\) RETURNING id`

// expectKeyCreatePreamble 铺 createKey 在 INSERT 之前的两条查询：应用已存在、同组没有
// 可用的旧密钥（pgx.ErrNoRows ⇒ findActiveKeyConflict 判「无冲突」）。
func expectKeyCreatePreamble(mock pgxmock.PgxPoolIface, tenantID, alias string) {
	mock.ExpectQuery(`SELECT id FROM applications WHERE code = \$1 AND tenant_id = \$2`).
		WithArgs("default", tenantID).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery(`FROM api_keys`).
		WithArgs(5, tenantID, alias).
		WillReturnError(pgx.ErrNoRows)
}

// keyInsertArgs 是 createKey 落库语句的期望参数：应用 id 与租户钉精确值（pgxmock 的
// 参数比对是类型敏感的 DeepEqual），其余列（哈希 / 前缀 / 密文 / 限额 / 备注 / 别名）只
// 钉「有这一位」。末尾可再追加 team_id 的匹配器。
func keyInsertArgs(extra ...any) []any {
	args := []any{5, "tenant-a"}
	for i := 0; i < 8; i++ {
		args = append(args, pgxmock.AnyArg())
	}
	return append(args, extra...)
}

// teamIDArg 钉最后一个参数就是请求里的 team_id（createKey 传的是 *int64）。
type teamIDArg int64

func (a teamIDArg) Match(v any) bool {
	p, ok := v.(*int64)
	return ok && p != nil && *p == int64(a)
}

// callCreateKey 以真实登录用户（tenant_admin）的身份调 createKey，返回响应与解码后的信封。
func callCreateKey(t *testing.T, h *Handler, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = SetAuthContext(req, &AuthContext{UserID: 7, TenantID: "tenant-a", Role: "tenant_admin"})
	rec := httptest.NewRecorder()
	h.createKey(rec, req)
	env := map[string]any{}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("200 响应必须是 JSON 信封: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, env
}

// TestCreateKey_PersonalKeyInsertOmitsTeamColumn：请求不带 team_id ⇒ 落库语句是 12 列、
// 10 个参数，**不含 team_id**。这条就是生产失败的那条路：语句多出一列，在没有迁移 057
// 的库上必然 42703。
func TestCreateKey_PersonalKeyInsertOmitsTeamColumn(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{keyCreateDB: mock, teamGuardDB: mock}

	expectKeyCreatePreamble(mock, "tenant-a", "personal-alias")
	mock.ExpectQuery(personalKeyInsertPattern).
		WithArgs(keyInsertArgs()...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(42))

	rec, env := callCreateKey(t, h, `{"key_alias":"personal-alias","budget_usd":1,"rate_limit_rpm":30}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("个人 key 必须建得出来（语句不得依赖 team_id 列）: got %d body=%s", rec.Code, rec.Body.String())
	}
	if env["id"].(float64) != 42 {
		t.Fatalf("必须回新建的 id: id=%v", env["id"])
	}
	if key, _ := env["api_key"].(string); !strings.HasPrefix(key, "sk-") || len(key) < 20 {
		t.Fatalf("必须回明文密钥（只在这一次响应里）: prefix=%q", key[:min(3, len(key))])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("落库语句必须恰好是期望的那一条: %v", err)
	}
}

// TestCreateKey_TeamKeyInsertWritesTeamColumn：请求带 team_id 且归属校验通过 ⇒ 落库语句
// 是 13 列、11 个参数，team_id 在最后。修复只让**个人** key 不依赖迁移；团队 key 的
// 写入路径原样保留（迁移应用之后它照常工作）。
func TestCreateKey_TeamKeyInsertWritesTeamColumn(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock.NewPool: %v", err)
	}
	defer mock.Close()
	h := &Handler{keyCreateDB: mock, teamGuardDB: mock}

	// 归属校验：同一事务里设 RLS GUC、查成员关系、回滚（与 TestCallerMayUseTeam_* 同形）。
	mock.ExpectBegin()
	mock.ExpectExec("set_config").
		WithArgs("7").
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(3), "tenant-a", int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectRollback()

	expectKeyCreatePreamble(mock, "tenant-a", "team-alias")
	mock.ExpectQuery(teamKeyInsertPattern).
		WithArgs(keyInsertArgs(teamIDArg(3))...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(43))

	rec, env := callCreateKey(t, h, `{"key_alias":"team-alias","budget_usd":1,"rate_limit_rpm":30,"team_id":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("团队 key 必须建得出来: got %d body=%s", rec.Code, rec.Body.String())
	}
	if env["id"].(float64) != 43 {
		t.Fatalf("必须回新建的 id: id=%v", env["id"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("落库语句必须恰好是期望的那一条: %v", err)
	}
}
