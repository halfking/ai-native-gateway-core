# llm-gateway-go Makefile
#
# 日常开发与 CI 流水线的入口。包含三类目标：
#   1. test - 单元/集成测试
#   2. build - 编译（包含新模块 cmd/sessionforensics）
#   3. lint - 静态检查
#   4. sessionforensics-* - 会话调试专项工具
#
# Usage:
#   make test                    # 全量单元测试（跟 CI 等价）
#   make test-short              # 短模式，跳过慢测试
#   make test-sessionforensics   # 仅 sessionforensics（含真实生产数据回放）
#   make build                   # 编译 + 产出 binaries
#   make lint                    # golangci-lint run
#   make sessionforensics-download ID=gw_xxx  # 从生产 admin 下载会话

GO ?= go
GOFLAGS ?= -trimpath
BIN_DIR := bin

# 把 /tmp/session_export/sessions 设进 ABS_SESSIONS_DIR 以便真实数据回放
ABS_SESSIONS_DIR ?= $(CURDIR)/tests/session_replay/sessions

# ── Default target ────────────────────────────────────────────────────────
.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示帮助
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2}'

# ── 测试 ──────────────────────────────────────────────────────────────────

CORE_GO_PACKAGES := ./internal/ir ./domains/dispatch ./domains/streaming/...

.PHONY: test
test: ## 全量单元测试（CI 默认入口）
	$(GO) test ./... -count=1 -timeout=300s

.PHONY: test-short
test-short: ## 短模式，跳过 -short=false 的测试
	$(GO) test ./... -count=1 -short -timeout=120s

# ── 审计守卫（R69）────────────────────────────────────────────────────
#
# 这些包是**静态审计门**，不是普通单元测试：它们在真实 DB / 供应商都不可用
# 的情况下也必须能跑（实测四个合计约 6s，无任何外部依赖），因此可以被放进
# pre-push 这类每次都执行的路径。
#
# R69 的发现：它们此前**没有被任何强制路径执行**——pre-push 的 Go 测试是
# opt-in（RUN_GO_TESTS=1）且只跑 licensing + envinjector，CI 各 workflow
# 也没有覆盖。于是「守卫」当时只是文档，不是门。
#
# 维护纪律：新增审计守卫包时必须同时登记到 GUARD_PACKAGES。`guards-sync`
# 门会检查有没有漏登记——否则「加了个守卫但没人跑」会再次悄悄发生。
# 单行定义：scripts/checks/guards-sync.sh 用 sed 取这一行，多行续行会让它
# 只读到前半段（这正是本脚本第一次跑就误报的��因）。
# R89-DY（211 号）：补登记 ./sql/schema。
#
# 为什么要补：这个包里的门（208 号 storage_reclaim_guard_test.go、209/210 号
# handrun_sql_inventory_test.go、objects_registry_test.go）在**任何 CI job 里都不会跑** ——
#   ① `make guards` 只跑本行登记的包，而本行原先只有 ./internal/*；
#   ② 仓库里**没有**任何 workflow 跑裸 `go test ./...`；
#   ③ 唯一跑全仓的是 integration job 的 `go test -tags=integration ./...`，而它的
#      包清单是用「有 integration-tagged 测试文件」做集合差**推导**出来的
#      （integration-testcontainers-ci.yml:210-218 写明了推导规则）⇒ 按构造就排除了
#      没有 integration 标签的 ./sql/schema。
# ⇒ 四个门只在本机 `go test ./sql/schema` 时才跑过；**兜底机制存在却不在任何流水线上**。
# 本机实测（含 -count=1 -timeout=120s，与 make guards 同一预算）：sql/schema 25.3s、
# 全 11 包墙钟 27s ⇒ `-timeout` 是**每个测试二进制**各自计时，25s 远低于 120s。
# ⚠️ scripts/checks/guards-sync.sh 用 `grep '^\./internal/'` 过滤已登记项，
#   所以加这一条**不会**让 guards-sync 误判。
# R89-EH（218 号）：补登记 ./internal/ingressguard。
# 为什么不直接给 domains/streaming 加门：它全量测试实测 ok 106.471s（墙钟 110s），
# 而本 target 是 -timeout=120s ⇒ 余量只有 13.5s，而 sql/schema 同机已在 25s~83s
# 之间波动（216 号）⇒ 登记它等于给 CI 埋一个「只在慢机器上炸」的雷。
# ingressguard 刻意**不导入** domains/streaming，只做静态文本判据，实测 0.4~0.7s。
# ⚠️ 本条**必须**登记：guards-sync.sh 是按磁盘上 `find internal -name '*guard'`
#   **发现式**枚举的（CI 的 audit-guards-ci.yml 第 87 步会跑它）⇒ 漏登记直接让 CI 转红。
# R89-EJ（219 号）：补登记 ./internal/healthstateguard。
# 它钉住「admin 恢复入口的状态层覆盖」：DB 四张表 + 路由缓存 + key 缓存 +
# key rotator + 进程内熔断器/fpslot + URSM v2，共 9 层，逐层必须表态。
# 存在理由：219 号查出「按供应商批量修复」入口只清 DB 侧、不清进程/Redis 侧，
# 却照样返回 HTTP 200 与「已恢复」语义（待裁决 85）。
# ⚠️ 同样**必须**登记：guards-sync.sh 按磁盘 `find internal -name '*guard'`
#   **发现式**枚举 ⇒ 漏登记直接让 CI 转红。
GUARD_PACKAGES := ./internal/rowsguard ./internal/errdiscard ./internal/dbrows ./internal/jsoncol ./internal/paramguard ./internal/sqlguard ./internal/sqlreadguard ./internal/metricguard ./internal/partguard ./internal/routeguard ./internal/ingressguard ./internal/healthstateguard ./internal/billguard ./internal/jsonbguard ./sql/schema

.PHONY: guards
guards: guards-installer ## 运行全部审计守卫（快速、无外部依赖；含 installer 跨模块门）
	$(GO) test $(GUARD_PACKAGES) -count=1 -timeout=120s

# R48-B1 后续（R48 报告 §五-3）：installer 是独立 go.mod，根目录
# `go test ./...` 永远不跑它的门——tsv 派生清单缺 831 的红因此在
# 「根目录全绿」下潜伏了一个月量级。挂进 guards 主门闭合跨模块盲区。
# 嵌套模块必须 cd 进去跑（R45 教训）。约 20~30s。
.PHONY: guards-installer
guards-installer: ## installer 子模块全量测试（独立 go.mod，跨模块门禁）
	cd installer && $(GO) test ./... -count=1 -timeout=300s

.PHONY: guards-sync
guards-sync: ## 校验：internal/ 下每个 *guard 包都已被登记进 GUARD_PACKAGES
	@bash scripts/checks/guards-sync.sh

.PHONY: test-race-core
test-race-core: ## 核心 IR/调度/流式包的 race 检测（不含外部依赖集成测试）
	$(GO) test -race $(CORE_GO_PACKAGES) -count=1 -timeout=600s

.PHONY: vet-core
vet-core: ## 核心 IR/调度/流式包的 go vet 检查
	$(GO) vet $(CORE_GO_PACKAGES)

.PHONY: bench-core
bench-core: ## 核心 IR/调度/流式包的 benchmark（不运行普通测试）
	$(GO) test $(CORE_GO_PACKAGES) -run '^$$' -bench . -benchmem -count=1 -timeout=600s

# fuzz 失败样本回归流程（见 scripts/fuzz/README.md）
FUZZTIME ?= 5s

.PHONY: fuzz-smoke
fuzz-smoke: ## 对全部 IR fuzz 目标限时运行（FUZZTIME=5s 可调）
	bash scripts/fuzz/fuzz-regress.sh run $(FUZZTIME)

.PHONY: fuzz-corpus-lint
fuzz-corpus-lint: ## 对已入库 fuzz 语料做脱敏门禁
	bash scripts/fuzz/sanitize_corpus.sh --all

.PHONY: bench-baseline
bench-baseline: ## 运行 bench-core 并更新基线文件（docs/perf/bench-baseline.txt）
	bash scripts/perf/bench_compare.sh --update

.PHONY: bench-check
bench-check: ## 对照基线检查 benchmark 回归（阈值见 scripts/perf/README.md）
	bash scripts/perf/bench_compare.sh --check

.PHONY: integrity-smoke
integrity-smoke: ## model integrity + durable queue planner smoke (requires PG*)
	bash scripts/integrity_smoke_test.sh

.PHONY: test-rls
test-rls: ## 使用 TEST_DATABASE_URL 运行真实 PostgreSQL RLS 门禁
	@test -n "$(TEST_DATABASE_URL)" || (echo "TEST_DATABASE_URL is required; source .env.local first" && exit 1)
	$(GO) test ./admin -run "^Test(RLS_|AssertSessionOwnerAccessInTx_RealDB)" -count=1 -v

.PHONY: test-pg-contracts
test-pg-contracts: ## 隔离 PostgreSQL 合约门禁（需要两个专用、低权限 DSN）
	@test -n "$${TEST_DATABASE_URL}" || (echo "TEST_DATABASE_URL is required" && exit 1)
	@test -n "$${TEST_TENANT_DATABASE_URL}" || (echo "TEST_TENANT_DATABASE_URL is required" && exit 1)
	@test "$${TEST_DATABASE_URL}" != "$${TEST_TENANT_DATABASE_URL}" || (echo "separate writer and tenant DSNs are required" && exit 1)
	TEST_PG_CONTRACTS_ISOLATED=1 $(GO) test -tags=integration ./bg -run '^TestProviderErrorAggregatorRealPG$$' -count=1 -v -timeout=90s
	TEST_PG_CONTRACTS_ISOLATED=1 $(GO) test -tags=integration ./domains/requestjourney -run '^TestJournalSnapshotReceiptRealPG$$' -count=1 -v -timeout=90s

# 真库判据（bg/*_realdb_test.go 一族）的显式运行入口，与 test-rls 同族。
#
# ★ 登记在这里的理由（2026-10-07）：该脚本一度只存在于文件系统里，
#   全仓 grep 除它自身外**零命中** ⇒ 没有任何入口提到它 ⇒ 它不会有人跑。
#   「写了脚本」不等于「门在那里」——一个没人知道的命令等于没有命令。
#
# 为什么不并进 pre-commit-check.sh：它连的是真生产库，
# 让每次提交都能打生产库是**权限问题**，不是门禁松紧问题。
# 所以它是显式登记的独立命令，形态与 test-rls 一致。
#
# 用法：
#   make test-realdb-gate
#   TEST_DATABASE_URL='postgres://...' make test-realdb-gate
#   scripts/run-realdb-gate.sh --list      # 只列登记了哪些，不连库
#
# ⚠ 无 DSN 时脚本退出码是 **2**，不是 0 ⇒ 「没跑成」与「跑了且通过」
#   在 make 层也可区分（不要只看 make 成功与否，先看退出码）。
.PHONY: test-realdb-gate
test-realdb-gate: ## 运行 bg 真库判据（需 TEST_DATABASE_URL/TEST_DB_URL；缺 DSN 退出码 2）
	@bash scripts/run-realdb-gate.sh

.PHONY: test-sessionforensics
test-sessionforensics: ## sessionforensics 全套（含真实数据回放）
	ABS_SESSIONS_DIR="$(ABS_SESSIONS_DIR)" $(GO) test ./domains/sessionforensics/... ./tests/session_replay/... -count=1 -v

.PHONY: audit-sessionforensics
audit-sessionforensics: ## 会话回放与六类场景审计（合成数据 + 本地导出数据）
	ABS_SESSIONS_DIR="$(ABS_SESSIONS_DIR)" $(GO) test ./domains/sessionforensics/... ./domains/hooks/outputcompliance/... ./domains/security/plugins/... ./domains/analysis/... ./admin/... ./bg/... ./tests/session_replay/... -count=1 -v

.PHONY: test-replay
test-replay: ## tests/session_replay 数据回放（依赖 ABS_SESSIONS_DIR）
	ABS_SESSIONS_DIR="$(ABS_SESSIONS_DIR)" $(GO) test ./tests/session_replay/... -count=1 -v

.PHONY: test-coverage
test-coverage: ## 跑测试并输出覆盖率（HTML）
	$(GO) test ./... -count=1 -coverprofile=coverage.out -timeout=300s
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "coverage: coverage.html"

# ── Build ─────────────────────────────────────────────────────────────────

.PHONY: build
build: ## 编译全部（含 cmd/sessionforensics）
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/sessionforensics ./cmd/sessionforensics
	$(GO) build ./...

.PHONY: build-only
build-only: ## 只编译 gateway 主二进制
	$(GO) build ./cmd/...

# ── Lint ──────────────────────────────────────────────────────────────────

.PHONY: lint
lint: ## golangci-lint run
	golangci-lint run --timeout=5m

# ── SessionForensics 专用 ─────────────────────────────────────────────────

.PHONY: sessionforensics-build
sessionforensics-build: ## 编译 cmd/sessionforensics CLI
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/sessionforensics ./cmd/sessionforensics

.PHONY: sessionforensics-help
sessionforensics-help: ## 显示 CLI 用法
	$(BIN_DIR)/sessionforensics help

.PHONY: sessionforensics-list
sessionforensics-list: ## 列最近活跃 session（需要 --from / --bearer 或 env）
	@echo "usage: $(BIN_DIR)/sessionforensics list --from=<URL> --bearer=<TOKEN> --limit=20"

.PHONY: sessionforensics-download
sessionforensics-download: ## 下载指定会话到本地（需要 --from / --bearer 或 env）
	@test -n "$(ID)" || (echo "ID=<gw_session_id> is required" && exit 1)
	$(BIN_DIR)/sessionforensics download \
	  --id="$(ID)" \
	  --out="$(ABS_SESSIONS_DIR)"

.PHONY: sessionforensics-replay
sessionforensics-replay: ## 本地回放某个会话（默认 128K window）
	@test -n "$(IN)" || (echo "IN=<file> is required" && exit 1)
	$(BIN_DIR)/sessionforensics replay --in="$(IN)" \
	  --model="$(or $(MODEL),gpt-4o)" \
	  --window="$(or $(WINDOW),128000)"

.PHONY: sessionforensics-summarize
sessionforensics-summarize: ## 生成摘要 + 标题（fallback 链）
	@test -n "$(IN)" || (echo "IN=<file> is required" && exit 1)
	$(BIN_DIR)/sessionforensics summarize --in="$(IN)"

.PHONY: sessionforensics-migrate
sessionforensics-migrate: ## 跨主机迁移：host A → host B
	@test -n "$(ID)" && test -n "$(FROM)" && test -n "$(TO)" || \
	  (echo "ID=, FROM=, TO= are required" && exit 1)
	$(BIN_DIR)/sessionforensics migrate \
	  --id="$(ID)" \
	  --from="$(FROM)" \
	  --to="$(TO)" \
	  --bearer="$(or $(BEARER),$$SESSION_FORENSICS_BEARER)"

# ── Data refresh ───────────────────────────────────────────────────────────

.PHONY: refresh-sessions
refresh-sessions: ## 从 252 生产 admin 重新拉取会话（需要 SSH 凭据）
	@echo "运行 extract.py → tests/session_replay/sessions/"
	@command -v python3 >/dev/null || (echo "python3 not found" && exit 1)
	python3 scripts/extract_sessions_from_prod.py \
	  --ssh-host 115.29.212.252 --ssh-port 25022 \
	  --pg-host 172.16.2.210 --pg-port 5432 \
	  --out "$(ABS_SESSIONS_DIR)"

.PHONY: report-mutations
report-mutations: ## 生成 7 种 mutation 的 Markdown + HTML 对比报告
	@echo "1) 跑 mutation_report_test.go 生成 7 份 JSON"
	ABS_SESSIONS_DIR="$(ABS_SESSIONS_DIR)" $(GO) test ./domains/sessionforensics/ -run "TestMutationReport_AutoGenerate" -count=1 -v
	@echo ""
	@echo "2) 渲染 Markdown + HTML index"
	python3 scripts/generate_mutation_report.py
	@echo ""
	@echo "✓ Done. Open tests/session_replay/sessions/reports/mutation/index.html"

.PHONY: open-mutation-report
open-mutation-report: ## 在浏览器打开 mutation 报告
	@command -v open >/dev/null && open "tests/session_replay/sessions/reports/mutation/index.html" || \
	  echo "Open tests/session_replay/sessions/reports/mutation/index.html manually"

# ── Helpers ──────────────────────────────────────────────────────────────

.PHONY: clean
clean: ## 清理 bin/、coverage.* 与根目录构建产物
	rm -rf $(BIN_DIR) coverage.out coverage.html
	rm -f gateway llm-gateway llm-gateway-linux-amd64 migrate-ursm-v2 routing-test-client
	rm -f *.test
