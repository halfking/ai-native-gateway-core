package admin

// 2026-10-02 批判式审计第三轮：把真库不变式门从「手写 SQL 副本」改成
// 「直接跑 handleRoutingResolve 的真实查询」。
//
// 前两轮已实证的两个缺陷（均为变异验证得出，非推测）：
//
//   缺陷 A —— 门不覆盖接线。TestResolveCandidatesInvariant_Live 调的是纯函数
//     filterResolveCandidatesByCid + 门自己手写的 SQL，**从不经过 handler**。
//     实测：删掉 handler 里 `candidates = filterResolveCandidatesByCid(...)`
//     整行，全包仍全绿。
//
//   缺陷 B —— 手写副本会与真 SQL 漂移。门内复刻了 handler 的三条 WHERE 分支。
//     实测：删掉 handler 里的 `OR lower(mc.canonical_name) = ANY($1)`，门仍全绿。
//
// 第一轮的修法是**在门外加两道 AST 门**（routing_resolve_wiring_test.go），
// 钉住「接线存在」与「三条匹配臂存在」。本轮问的是更靠上游的问题：
// **能不能让门根本不需要副本？**
//
// 实测本文件与旧门的差异（对同一 DSN，同一 960 个目录模型）：
// 旧门那份手写 SQL 相对 handler 真实查询少了三处**结构性**内容：
//   1. 缺 `JOIN credentials c`（真实查询是 INNER JOIN，会剔除无凭据行）；
//   2. 缺 `JOIN providers p` 与 `JOIN credential_model_bindings cmb`；
//   3. 缺全部租户谓词 `($2 = '' OR v.tenant_id = $2) AND v.tenant_id = p.tenant_id
//      AND v.tenant_id = c.tenant_id`。
// 前两轮那道 AST 漂移门**看不到这三种漂移** —— 它只钉三条 `= ANY($1)`
// 匹配臂的存在，删掉一个 JOIN 它照样绿。
//
// 本门的新性质（写明以便判断它替代了什么、没替代什么）：
//   * 需要 DB（本来就是真库门）；另外需要 httptest 请求夹具。
//   * **不开 DSN 时不再能跑** —— 但旧的纯函数门（TestCanonicalIDByVariantPriority_*）
//     仍然是独立的、不碰 DB 的门，那部分性质没有被这条拿走。
//   * handler 会写库：singleCanonicalForRevision 命中时走
//     ensureCanonicalScopeRevision / ensureScopeRevision，二者都是
//     `ON CONFLICT DO NOTHING` 的**幂等 seed**，不是 bump。因此本门对
//     candidate_binding_scope_revision_canonical / _raw_model 是幂等写入，
//     重复跑不产生漂移。这一点是本门可以安全跑真实 handler 的前提。
//   * h.ursmV2 为 nil → 走 applyResolveDefaults 分支（DB 字段降级默认值）；
//     h.liveRouting 为 nil → livePlanOrder 返回 "unavailable"。
//     两者都不影响 candidates 的 canonical_id，故不变式判定不受影响。
//
// 已知边界（明确本门**看不到**什么，其中一条是变异验证的负面结果）：
//   * 它验证的是 handler 返回的 candidates 里没有 foreign canonical_id。
//     它**不**验证 raw_models 泄漏（那是 TestResolveRawModelsStillLeak 的职责，
//     仍是已知的、自陈的边界）。
//   * 它不覆盖 persist_probe 分支（需 `?persist_probe=1` 且会写决策日志），
//     该分支的双保险过滤仍只有源码注释与上一轮的接线门守着。
//   * 租户维度：裸 httptest 请求 ⇒ EffectiveTenantIDAll 返回 ""（查全部租户），
//     与 super_admin 语义一致。**tenant_admin 的收窄视图不被本门覆盖**。
//
//   * **它抓不到「召回面变小」这一类漂移**（变异验证的负面结果，2026-10-02）。
//     注入 `OR lower(mc.canonical_name) = ANY($1) AND false`，即让真实查询
//     少一个匹配面：本门与旧门、与上一轮的 AST 漂移门**三者全绿**。
//     原因是本门断言的是「不出现 foreign」这一**上界性质**：丢掉一个匹配臂
//     只会让候选变少，不会凭空引入外部 canonical_id。
//     ⇒ 对「真 SQL 少一个匹配臂」这一族漂移，仍然只有
//       routing_resolve_wiring_test.go 的 AST 门在守。**两道门互不替代**：
//       本门守「过滤后的结果干净」，AST 门守「匹配面没被删」。
//     不要因为本门存在就删掉那道 AST 门 —— 那正是它唯一承重的场景。

// 变异验证（2026-10-02 实测，两处串行，每处注入前都确认上一处已还原）：
//
//  变异 1 —— 删掉 handler 里的 `candidates = filterResolveCandidatesByCid(candidates, expectedCid)`
//            （保留 resolveInputCanonicalID 调用以便编译通过，即第一轮实证过的
//            「全包 76.5s 全绿」那个变异）：
//              本门：FAIL，156 处 INVARIANT BROKEN  ✅ 承重
//              旧门 TestResolveCandidatesInvariant_Live：ok  ❌ 依旧全绿
//            ⇒ 决定性证据：本门真的经过 handler，旧门是自洽闭环。
//
//  变异 2 —— 让真实查询少一个匹配面（`OR lower(mc.canonical_name) = ANY($1) AND false`）：
//              本门：ok  ❌
//              旧门：ok  ❌
//              上一轮 AST 漂移门：ok（它只钉该臂「存在」，不钉其语义）
//            ⇒ 负面结果，如文件头「已知边界」所述：对这一族漂移只有 AST 门承重。
//
//  两处变异均已还原（`git diff` 干净、变异标记 grep 归零）。
//
// 独立复验（2026-10-03 06:54 CST，另一轮会话重做，非沿用上述结论）：
//  起因是「DSN-gated SKIP」有可能是**恒跳过的空壳**——若 CI/本地从未设
//  TEST_RESOLVE_INVARIANT_DB_URL，本门会一直 t.Skip，而文档里所有「实测绿」
//  都可能只是 SKIP。实测三步：
//   1) 设 DSN（.env.local 的 LLM_GATEWAY_DATABASE_URL，注意门要的是
//      **TEST_RESOLVE_INVARIANT_DB_URL**，不是 LLM_GATEWAY_DATABASE_URL）
//      实跑 → **PASS**（非 SKIP），逐模型列出，13.9s ⇒ 门体有真断言、
//      不是装饰。
//   2) 重放变异 1（同一处 `routing.go:469`，标记 grep 确认恰好 1 处）
//      → **FAIL，156 处 INVARIANT BROKEN**，与上一轮数字**完全一致**
//      ⇒ 承重性可复现，不是上一轮的一次性巧合。
//   3) 还原 → 标记归零、`git diff` 干净、真库门与 AST 门皆 ok。
//  ⚠️ 关于「CI 里会不会 SKIP」——已查证（2026-10-03 06:58），结论是**会跑**：
//   * `scripts/audit/run-integration-gate.sh:587` 显式注入
//     `TEST_RESOLVE_INVARIANT_DB_URL="$GATE_URL"`（连同另外 12 个 DSN 变量）。
//   * `integration-testcontainers-ci.yml:244` 逐包调用该脚本；包列表由
//     `derive-gate-packages.sh` 派生，实测 `./admin` **在**该列表内
//     （该包另有 10+ 个 integration-tag 测试文件，满足派生条件）。
//   * 本门**无 build tag**，故不受脚本 `-tags=integration` 影响；
//     同包的旧门与 AST 门同样无 tag，三者都会在这个 gate 下运行。
//   ⇒ 无 DSN 时会 SKIP 的只有「本地/CI 之外随手跑 `go test ./admin/`」这种情况；
//     正式门禁路径上本门是承重的。
//
//  与旧门的关系：**旧门不删**。旧门是「手写副本 + 纯函数」的自洽闭环，
//  它测不到 handler；本门测 handler。删掉旧门不会增加任何保证，只会少一条
//  在无 handler 依赖时也能跑的参照。两者并列时才构成完整覆盖。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/modelname"
)

// resolveHandlerResponse is the subset of handleRoutingResolve's JSON body the
// invariant needs. Decoding into a narrow struct (rather than map[string]any)
// keeps the gate honest: if the handler renames or drops `candidates`, this
// unmarshals to nil and the gate reports it instead of silently passing.
type resolveHandlerResponse struct {
	CanonicalName string   `json:"canonical_name"`
	CanonicalID   *int64   `json:"canonical_id"`
	RawModels     []string `json:"raw_models"`
	Candidates    []struct {
		CredentialID int    `json:"credential_id"`
		ModelName    string `json:"model_name"`
		CanonicalID  *int64 `json:"canonical_id"`
	} `json:"candidates"`
}

// callResolveHandler drives the REAL handler in-process and returns its decoded
// body. It takes *Handler by value pointer so h.db is the same pool the caller
// already opened; no auth context is injected, which is deliberate (see the
// file header: it means "all tenants", the widest view — the invariant must hold
// at the widest scope or it does not hold).
func callResolveHandler(t *testing.T, h *Handler, model string) resolveHandlerResponse {
	t.Helper()
	// Catalog names legitimately contain spaces ("claude sonnet 5 …"), which
	// httptest.NewRequest rejects in a raw URL. Percent-encode the value.
	req := httptest.NewRequest(http.MethodGet,
		"/api/routing/resolve?model="+url.QueryEscape(model), nil)
	rec := httptest.NewRecorder()
	h.handleRoutingResolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("resolve(%q) returned HTTP %d: %s", model, rec.Code, rec.Body.String())
	}
	var out resolveHandlerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("resolve(%q) body is not the expected JSON shape: %v", model, err)
	}
	if out.Candidates == nil {
		t.Fatalf("resolve(%q) returned no `candidates` key (or an explicit null) — "+
			"the response shape changed, so this gate would measure nothing", model)
	}
	return out
}

// TestResolveCandidatesInvariant_ThroughHandler is the root-cause gate for
// defects A and B: it drives handleRoutingResolve itself, so there is no
// hand-written SQL copy that can drift and no pure-function shortcut that can
// bypass the wiring.
//
// The assertion is the same invariant the 2026-09-29 changelog claimed:
//
//	resolve('X') 返回的所有 candidate.canonical_id 都属于 resolve_input('X')
//
// Load-bearing check (the mutation this gate was built to catch): deleting
//
//	candidates = filterResolveCandidatesByCid(candidates, expectedCid)
//
// from handleRoutingResolve — the exact mutation that left the whole package
// green in round 1 — must turn THIS gate red. A gate that stays green under
// that mutation is measuring its own copy, which is defect B wearing a new hat.
func TestResolveCandidatesInvariant_ThroughHandler(t *testing.T) {
	dsn := os.Getenv(resolveInvariantDBEnv)
	if dsn == "" {
		t.Skipf("%s not set — SKIPPING the through-handler invariant gate. "+
			"A skip is NOT evidence: the invariant stays unproven until this runs "+
			"against a real models_canonical through the real HTTP handler.",
			resolveInvariantDBEnv)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT canonical_name FROM models_canonical
		WHERE canonical_name IS NOT NULL AND canonical_name <> ''
	`)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("catalog rows: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("empty models_canonical — the gate would be vacuous")
	}

	// Minimal handler: db is the only field the resolve path truly needs.
	// ursmV2/liveRouting stay nil on purpose (see the file header) so the
	// runtime overlays degrade to their documented defaults instead of
	// requiring Redis or a live router in a DB-only gate.
	h := &Handler{db: pool}

	var checked, withForeign int
	for _, model := range names {
		t.Run(model, func(t *testing.T) {
			inputCid, ok := resolveInputCanonicalID(ctx, pool, model)
			if !ok {
				return // fail-open path: handler filters nothing either
			}
			checked++

			// THE point of this gate: the candidate set comes from the real
			// handler, through the real query, with the real filter applied.
			out := callResolveHandler(t, h, model)

			var foreign []int64
			for _, c := range out.Candidates {
				// NULL canonical_id = legacy binding, deliberately kept.
				if c.CanonicalID == nil || *c.CanonicalID == inputCid {
					continue
				}
				foreign = append(foreign, *c.CanonicalID)
			}
			if len(foreign) > 0 {
				withForeign++
				sort.Slice(foreign, func(i, j int) bool { return foreign[i] < foreign[j] })
				t.Errorf("INVARIANT BROKEN for %q: resolve_input canonical_id=%d but "+
					"handleRoutingResolve returned %d candidates carrying foreign "+
					"canonical_id(s) %v (response canonical_name=%q raw_models=%v)",
					model, inputCid, len(foreign), foreign,
					out.CanonicalName, modelname.NormalizeRouteKeyAliases(model))
			}
		})
	}
	if checked == 0 {
		t.Fatal("no model resolved to a canonical_id — the gate proved nothing")
	}
	t.Logf("invariant verified through the real handler for %d/%d catalog models "+
		"(%d with foreign candidates)", checked, len(names), withForeign)
}
