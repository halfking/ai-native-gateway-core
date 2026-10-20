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
# ⚠ GATEWAY_CANONICAL_TEST_DSN **不在事实源里**（R85 轮）：
#   它是 dsn_exclusive（专用值）—— canonical fixture 除主库外**还要连第二个库**
#   `identity_shadow`（admin/shared_principal_realdb_test.go:113 改库名）
#   ⇒ ⇒ 它本来就只能指向「本地联调专用库」。
#   ⇒ 与通用 DSN 同值 = 把「连本地库」的前提给了一个通用库。
#   ⇒ 产品侧自己的守卫会正确地拒绝这种 DSN（授权开关未开 ⇒ t.Fatal）
#     ⇒ 事实源默认注入它，只会把「靶子不对」显示成红。
#   ★ 授权开关本身（GATEWAY_CANONICAL_TEST_LOCAL_ONLY，dangerous 类）**绝不由事实源
#     打开** ⇒ 门禁不能替人授予「连本地库」的权限。
# ⚠ GUARD_IT_DSN **不在事实源里**（R86 轮）：
#   它是 dsn_exclusive（专用值）—— bg/balance_floor_guard_integration_test.go:53
#   在建好池子后**裸 TRUNCATE credentials, providers CASCADE**
#   ⇒ 而该文件头自己写明「本测试绝不写线上 llm_gateway 库」。
#   ⇒ 与通用 DSN 同值 = 把「清空凭据表」这件事对准产品库。
#   ⇒ R86 轮同时给那条腿补了库名守卫（缺口：契约只写在注释里，代码里没有）。
export IDENTITY_SHADOW_DSN="$__datasource_dsn"
export LICENSE_AUTHORITY_DATABASE_URL="$__datasource_dsn"
export LLM_GATEWAY_DATABASE_URL="$__datasource_dsn"
export LLM_GATEWAY_DUALREAD_PG_DSN="$__datasource_dsn"
# ⚠ LLM_GATEWAY_FEATURE_STATS_TEST_DSN **不在事实源里**（R86 轮）：
#   它是 dsn_exclusive（专用值）—— bg/feature_stats_worker_test.go 的
#   setupTestDB 会 `TRUNCATE feature_distribution_stats, dedup_stats,
#   auto_route_selections CASCADE`，且库名守卫只放行
#   itgate|scratch|test|tmp|bench|pg_ ⇒ **通用 DSN（库名 llm_gateway）必被拒绝**。
#   ⇒ ⇒ 事实源注入它 = 注入一个**这条腿一定会拒绝**的值：
#      事实源档下它是**死腿**（SKIP），官方 runner 档下它**根本不设**（同样 SKIP）。
#   ⇒ ⇒ 两条通道都是死的 ⇒ 移出事实源**零覆盖损失**（移出前那条腿在两个通道里
#      都没跑过；移出后只是把「给了一个用不了的值」改成「明说没给」）。
#   ⇒ 真要跑它 ⇒ 显式 export 一个指向一次性库的 DSN。
export LLM_GATEWAY_PG_TEST_URL="$__datasource_dsn"
export LLM_GATEWAY_PG_URL="$__datasource_dsn"
export LLM_GATEWAY_ROLLUP_TEST_DSN="$__datasource_dsn"
export LLM_GATEWAY_ROUTING_GATE_PG_DSN="$__datasource_dsn"
export LLM_GATEWAY_SUPPLIER_PG_DSN="$__datasource_dsn"
export LLM_GATEWAY_TEST_PG_DSN="$__datasource_dsn"
export OMNIFREE_TEST_DB_ADMIN_URL="$__datasource_dsn"
export OMNIFREE_TEST_DB_URL="$__datasource_dsn"
# ⚠ ROUTEINCIDENT_IT_DSN **不在事实源里**（R86 轮）：
#   它是 dsn_exclusive（专用值）—— domains/routeincident/store_pending_lifecycle_integration_test.go
#   会在目标库里 `CREATE TABLE IF NOT EXISTS route_incidents`（715 表形）
#   ⇒ 该文件头写明「Cleanup: DROP DATABASE llm_routeincident_it」⇒ 设计目标是一次性库。
#   ⇒ 与通用 DSN 同值 = 往产品库里塞一张集成测试专用表（污染 + 迁移 715 会撞它）。
#   ⇒ R86 轮同时给那条腿补了库名守卫。
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
# ⚠ TEST_SESSION_INGEST_DATABASE_URL **不在事实源里**（R86 轮）：
#   它是 dsn_exclusive（专用值）—— internal/sessionv2mirror/ingest_schema_realdb_test.go:40
#   的守卫要求「库名以 _test 结尾」**且** `TEST_SESSION_INGEST_ISOLATED=1`，
#   随后 :49 跑 `DROP SCHEMA public CASCADE` ⇒ **它会清空整个 public schema**。
#   ⇒ 通用 DSN（库名 llm_gateway）不满足后缀 ⇒ 守卫 t.Fatal（响亮假红，不是数据事故）。
#   ⇒ 移出事实源 ⇒ 那条腿改走它本来就设计好的 t.Skip 分支（"set … to a throwaway *_test database"）。
#   ⇒ ★ 隔离开关 `TEST_SESSION_INGEST_ISOLATED` 属 isolation 类 ⇒ **绝不由事实源打开**。
# ⚠ TEST_SESSION_SETTINGS_DATABASE_URL **不在事实源里**（R86 轮）：
#   它是 dsn_exclusive（专用值）—— settings/session_write_safety_realdb_test.go:24
#   要求「库名以 _test 结尾」**且** `TEST_SESSION_INGEST_ISOLATED=1`（隔离开关），
#   随后 :31 跑 `DROP SCHEMA public CASCADE`。
#   ⇒ 通用 DSN（llm_gateway）两个条件都不满足 ⇒ 守卫 t.Fatal（响亮假红）。
#   ⇒ 移出事实源 ⇒ 那条腿改走它本来就设计好的 t.Skip 分支
#     （"set TEST_SESSION_SETTINGS_DATABASE_URL to isolated *_test database"）。
#   ⇒ ★ 隔离开关仍**绝不由事实源打开**（它是 isolation 类，见登记册）。
# ⚠ TRACE_RETIREMENT_TEST_DSN **不在事实源里**（R81 轮）：
#   它是 dsn_exclusive（专用值）—— 必须指向名为 trace_retirement* 的隔离库
#   （或显式授权的本地 54318 schema）。
#   ⇒ 与通用 DSN 同值 = 让破坏性的 trace-retirement 清理打到共享库上。
#   ⇒ 产品侧自己的守卫（trace_retirement_test.go 等四处）会拒绝这种 DSN
#   ⇒ 事实源默认注入它，只会把「真库腿跑不起来」变成「门禁红」。

unset __datasource_dsn
