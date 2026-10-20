#!/usr/bin/env bash
# ── datasource_env.sh ────────────────────────────────────────────────────────
# 真库腿的 DSN 环境变量 **单一事实源**（R69 轮新建）。
#
# 为什么需要它（R69 轮实测，三条独立证据）：
#
#   ① ★★★★ **官方 gate runner 自己漏了 21 个变量名**。
#      `scripts/audit/run-integration-gate.sh` 的 `env \` 块只导出 20 个名字，
#      而「打开 PG 的测试文件」一共认 **40 个**（本文件清单）⇒
#      **≥24 条真库腿在官方跑法下拿不到 DSN**。
#      实测（`itgate_r69_c`）：`TestFeatureStatsWorkerDedupRate`、
#      `TestSessionRetirementHTTPRealDB` 在官方 env 集下 **SKIP**。
#
#   ② ★★★★ **「静默 SKIP」有两种形态，`go test` 都把它们算成绿**：
#      (a) 「变量没设 ⇒ t.Skip」；
#      (b) 「变量没设 ⇒ 去连一个**默认/写死**的库 ⇒ 连不上 ⇒ 仍然 Skip」。
#      (b) 的实例：`bg/feature_stats_worker_test.go:341`
#          `dsn = "postgres://localhost/llm_gateway_test?sslmode=disable"`
#          ⇒ 官方 runner 漏名 ⇒ 走 fallback ⇒ 认证失败 ⇒ SKIP ⇒ **记成绿**。
#      ★ 本仓的 SKIP 消息本身是诚实的（都明说「跳过不构成证据」，
#        例如 `routing_resolve_invariant_test.go`：
#        「A skip is NOT evidence: the invariant stays unproven …」），
#        **问题只在「变量集从哪来」**。
#
#   ③ ★★★★ **人写的清单必然漏** —— 连本文件的**第一版**都漏了
#      （第一版只抄了官方 runner 的 20 个 ⇒ 实测 `TEST_RETIREMENT_HTTP_DSN`
#      仍然 SKIP ⇒ 才发现它不在清单里）。
#      ⇒ ⇒ 所以必须有一道**自动对账的门**
#      （`scripts/ursmcheck/datasource_env_test.go`），
#      **不能靠人记得全**。
#
# 用法：
#   source scripts/audit/datasource_env.sh "postgres://user:pass@host:5432/db?sslmode=disable"
#   go test -tags=integration ./admin/...
#
# 清单口径（门 `scripts/ursmcheck/datasource_env_test.go` 逐条钉住）：
#   · **来源** = 「打开 PG 连接的测试文件」认的全部 DSN 类环境变量名
#     ∪ `run-integration-gate.sh` 的 env 块（不回退权威清单）；
#   · **排除** = 下面「显式豁免」5 个（Redis / HTTP / 开关 / 租户角色）。
#
# ⚠ **显式豁免**（不进本清单，理由见括号）：
#   · TEST_REDIS_URL            （Redis DSN —— 指过去比留空更糟）
#   · NETBIRD_API_URL           （HTTP API 端点，不是 DSN）
#   · XIAOMI_BASE_URL           （HTTP base URL，不是 DSN）
#   · WA877_ALLOW_ANY_DSN       （是「允许任意 DSN」的开关，不是 DSN 本身）
#   · TEST_TENANT_DATABASE_URL  （必须指向**独立的、不可绕过 RLS 的租户角色**，
#                                由 run-integration-gate.sh 现场建 ⇒ 不能与通用 DSN 同值）
# ⚠ `TEST_REDIS_URL` 同样**故意不在这里**：把一个 PG DSN 指过去比不设更糟。
# -----------------------------------------------------------------------------

if [ "$#" -lt 1 ]; then
  echo "usage: source $0 <dsn>" >&2
  return 1 2>/dev/null || exit 1
fi

__datasource_dsn="$1"
if [ -z "$__datasource_dsn" ]; then
  echo "datasource_env.sh: empty DSN" >&2
  return 1 2>/dev/null || exit 1
fi

export AI_SESSION_MANAGER_DATABASE_URL="$__datasource_dsn"
export BOOTSTRAP_TEST_DSN="$__datasource_dsn"
export CONTEXT_ATTRS_TEST_DATABASE_URL="$__datasource_dsn"
export D07_S01_ADMIN_URL="$__datasource_dsn"
export D07_S01_PG_URL="$__datasource_dsn"
export DATABASE_URL="$__datasource_dsn"
export DB_TEST_URL="$__datasource_dsn"
export DECISION_RETIREMENT_TEST_DATABASE_URL="$__datasource_dsn"
export GATEWAY_CANONICAL_TEST_DSN="$__datasource_dsn"
export GUARD_IT_DSN="$__datasource_dsn"
export IDENTITY_SHADOW_DSN="$__datasource_dsn"
export LICENSE_AUTHORITY_DATABASE_URL="$__datasource_dsn"
export LLM_GATEWAY_DATABASE_URL="$__datasource_dsn"
export LLM_GATEWAY_DUALREAD_PG_DSN="$__datasource_dsn"
export LLM_GATEWAY_FEATURE_STATS_TEST_DSN="$__datasource_dsn"
export LLM_GATEWAY_PG_TEST_URL="$__datasource_dsn"
export LLM_GATEWAY_PG_URL="$__datasource_dsn"
export LLM_GATEWAY_ROLLUP_TEST_DSN="$__datasource_dsn"
export LLM_GATEWAY_ROUTING_GATE_PG_DSN="$__datasource_dsn"
export LLM_GATEWAY_SUPPLIER_PG_DSN="$__datasource_dsn"
export LLM_GATEWAY_TEST_PG_DSN="$__datasource_dsn"
export OMNIFREE_TEST_DB_ADMIN_URL="$__datasource_dsn"
export OMNIFREE_TEST_DB_URL="$__datasource_dsn"
export ROUTEINCIDENT_IT_DSN="$__datasource_dsn"
export ROUTING_ANALYTICS_GATE_TEST_DATABASE_URL="$__datasource_dsn"
export SANITIZE_RULES_BROWSER_DSN="$__datasource_dsn"
export TEST_ADMIN_500_DSN="$__datasource_dsn"
export TEST_AUDIT_FRESH_SCHEMA_DB_URL="$__datasource_dsn"
export TEST_AUDIT_ISOLATED_DB_URL="$__datasource_dsn"
export TEST_DATABASE_URL="$__datasource_dsn"
export TEST_DB_URL="$__datasource_dsn"
export TEST_G6_TOOLS_DSN="$__datasource_dsn"
export TEST_INSTALLER_FRESH_DB_URL="$__datasource_dsn"
export TEST_PG_DSN="$__datasource_dsn"
export TEST_PG_URL="$__datasource_dsn"
export TEST_RESOLVE_INVARIANT_DB_URL="$__datasource_dsn"
export TEST_RETIREMENT_HTTP_DSN="$__datasource_dsn"
export TEST_SESSION_INGEST_DATABASE_URL="$__datasource_dsn"
export TEST_SESSION_SETTINGS_DATABASE_URL="$__datasource_dsn"
export TRACE_RETIREMENT_TEST_DSN="$__datasource_dsn"

unset __datasource_dsn
