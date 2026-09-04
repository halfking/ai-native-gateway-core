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

.PHONY: test-race-core
test-race-core: ## 核心 IR/调度/流式包的 race 检测（不含外部依赖集成测试）
	$(GO) test -race $(CORE_GO_PACKAGES) -count=1 -timeout=600s

.PHONY: vet-core
vet-core: ## 核心 IR/调度/流式包的 go vet 检查
	$(GO) vet $(CORE_GO_PACKAGES)

.PHONY: bench-core
bench-core: ## 核心 IR/调度/流式包的 benchmark（不运行普通测试）
	$(GO) test $(CORE_GO_PACKAGES) -run '^$$' -bench . -benchmem -count=1 -timeout=600s

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
