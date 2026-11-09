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

# ─────────────────────────────────────────────────────────────────────────
# ★ 2026-11-09：**门文件数实时取，绝不写死**。
#
#   ★ 起因：本文件此前把「166 个门文件」写死在**两处**（一行注释 + 一行
#     `##` 帮助文本）⇒ ⇒ 实测当日已是 **180** ⇒ ★★ 写死的数字**不会自己过期**。
#   ★★ 而本仓**自己**已经判过这个死（r1050 轮，2026-10-30，§7.3 与 lesson #8）：
#     「**注释里的读数漂移**在结构上无法被自动发现 ⇒ 最好的修法不是「定期回填」
#      而是「**让数字不可能过期**」⇒ ★ 把 `guards-sync.sh` 那句 `echo "它有 94
#      个门文件"` 改成 `find` **实时数** ⇒ ★ **数字从此不可能过期**」。
#   ⇒ ★★ r1050 轮**只**修了 `scripts/checks/guards-sync.sh`（运行期 echo），
#     **漏了**本文件这两处 ⇒ ⇒ 本轮补齐，且用**同一套办法**（实时数）而不是
#     「把 166 改成 180」（★ 回填一次就会再过期一次）。
#
#   ★ `##` 帮助文本是**面向人的输出**（`make help` 可见）⇒ ★★ r1050 lesson #9：
#     「注释假话与运行期假话，价钱不同 ⇒ **活的假话比注释假话更贵**」
#     ⇒ ⇒ 所以下面用 `@URSMCK_GATE_COUNT@` 占位符 + `make help` 里的 `sed`
#     **实时替换** ⇒ ⇒ `make help` 打印的门文件数**永远是真的**。
#
#   ★ 口径：门文件 = `scripts/ursmcheck/` 下的 `*_test.go`（与
#     `scripts/checks/guards-sync.sh:55` 的实时数**同一口径** ⇒ 两处数字同源）。
#     ★ 实测该目录下**无**非测试 `.go` 文件 ⇒ 两种口径当前都等于 180。
# ─────────────────────────────────────────────────────────────────────────
URSMCHECK_GATE_COUNT := $(shell find scripts/ursmcheck -maxdepth 1 -name '*_test.go' -type f 2>/dev/null | wc -l | tr -d ' ')

# 把 /tmp/session_export/sessions 设进 ABS_SESSIONS_DIR 以便真实数据回放
ABS_SESSIONS_DIR ?= $(CURDIR)/tests/session_replay/sessions

# ── Default target ────────────────────────────────────────────────────────
.DEFAULT_GOAL := help

.PHONY: help
help: ## 显示帮助
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  sed 's|@URSMCK_GATE_COUNT@|$(URSMCHECK_GATE_COUNT)|g' | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2}'

# ── 测试 ──────────────────────────────────────────────────────────────────

CORE_GO_PACKAGES := ./internal/ir ./domains/dispatch ./domains/streaming/...

.PHONY: test
test: ## 全量单元测试（CI 默认入口）
	$(GO) test ./... -count=1 -timeout=1800s
# ★★ 2026-11-09 r1050 轮复核：本 target 的每包预算 **300s → 1800s**。
#   ★ 起因（★ 实测 + 推理）：★ 本 target 跑的是 `go test ./...` ⇒ ★★ **它包含
#   ★★ `./scripts/ursmcheck`**（★ 慢门那 180 个门文件所在包）⇒ ★★★ 而该包实测
#   ★★★ **1122.817s**（★ r1050 轮 run2，★ HEAD 5a4150440，★ 低负载档）⇒ ★★★
#   ★★★ **⇒ 300s 的预算对它必然腰斩** ⇒ ★★★ **本 target 一直是红的**（★ 红在
#   ★★★ `panic: test timed out`，★ 不是判据红）。
#   ★★★ 而**官方门 `verify.sh` 的全量腿早就是 `-timeout=1800s`**（★ r1044 轮
#   ★★★ 把它从 900s 提到 1800s）⇒ ★★★★ **⇒ 同一批包在两个入口上预算不同
#   ★★★★ ⇒ 正是本轮新立通用式「同一道门在不同通道上有不同预算」⇒ ⇒ ★★ 必须同档**。
#   ⚠ 提高的仍只是**每包**预算，不是总时长（★ 各包并行 ⇒ 墙钟由最慢的包决定）。

.PHONY: test-short
test-short: ## 短模式，跳过 -short=false 的测试（scripts/ursmcheck 静态门包已超出本通道预算，走 guards-ursmcheck / verify.sh）
	$(GO) test $$( $(GO) list ./... | grep -v '^github.com/kaixuan/llm-gateway-go/scripts/ursmcheck$$' ) -count=1 -short -timeout=1800s
# ★★ 2026-11-09 r1050 轮复核：**本 target 的每包预算 120s → 1800s**（★ 与 `test` /
#   ★ `guards` / `guards-fast` / `.githooks/pre-push` **五处同档** ⇒ ★★ 见 `guards`
#   ★★ target 上方那段预算依据）。
#   ★ 起因（★ 实测）：★ 本轮跑 `make test-short` ⇒ ★★★ **`admin` FAIL 120.552s**，
#   ★★★ 日志是 `panic: test timed out after 2m0s` ⇒ ★★★★ **又是腰斩、不是判据红**
#   ★★★ ⇒ ★★★ **⇒ 这是同一个预算失配的载体之一** ⇒ ⇒ ★★ 必须一起改。
#   ★★ **它不是「短模式所以一定快」**：★ `-short` 只跳过显式声明 `testing.Short()` 的用例；
#   ★★ 而 `internal/sqlguard` **一个 `testing.Short()` 都没有**（★ 实测 grep 零命中）
#   ★★ ⇒ ★★ **它在 `-short` 通道里照样要跑满 ~269s** ⇒ ⇒ ★★ **「短」是作者的意图，
#   ★★ 不是包的实际代价** ⇒ ★★ **预算必须按实际代价定**。
#   ★★ **为什么定 1800s 而不是 600s**：★ 本通道虽排除了 `scripts/ursmcheck`，★ 但
#   ★★ **它含 `admin`**（★ 481 个测试文件）⇒ ★★ 而 `admin` 在**慢门**里就是按
#   ★★ **1800s** 预算的 ⇒ ★★★★ **同一个包在两条通道上必须是同一个预算** ⇒ ★★★
#   ★★★ （★ 本轮 600s 那一次在**极端负载**（load 371）下仍被腰斩于 601.588s，
#   ★★★ ★ 腰斩点定位到单个重测试 `TestEveryV1ReaderIsAnalyzedByExtractor`（已跑 2m50s）
#   ★★★ ★ ⇒ ★★★ **⇒ 600s 在争用机器上并不够 ⇒ 直接对齐慢门的 1800s**）。
#   ★★ 另有既存登记（★ `LESSONS.md` §611 记「admin ~104s vs 120s 只剩 16s 余量」）
#   ★★ ⇒ ★★★ **该登记已过期**：★ 本轮实测 admin 在慢门里需 **162.700s**、在本通道
#   ★★★ 里被腰斩于 120.552s ⇒ ★★★ **⇒ 「余量 16s」的说法必须作废**。

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
# 只读到前半段（这正是本脚本第一次跑就误报的原因）。
# R89-DY（211 号）：补登记 ./sql/schema。
#
# ★ 2026-10-24 magpie 对齐轮补登记 5 个包（实测撞到「门写了没人跑」）：
#   ./internal/rewrite ./internal/routeorder ./internal/requestobserver
#   ./domains/credential ./domains/authentication
#   —— 这五个包的契约门（magpie 对齐轮新增）**原先一个都不在表里**
#   ⇒ CI 的 `make guards` 从不执行它们 ⇒「门是绿的」在 CI 上是空转。
#   ⇒ 判据见 scripts/ursmcheck/audit_guard_registration_test.go：
#      **门文件自己声明**（文件头 `AUDIT-GUARD:`）⇒ 其所在包必须被登记。
#   ⇒ 实测（干净环境，无 DB/Redis）：五包全绿，合计 15.7s（每包远低于 120s 上限）。
#   ⇒ 前三个包名不以 guard 结尾 ⇒ guards-sync.sh 的命名检查会**告警**（不拦），
#      那是因为它们是**契约包**（被生产代码引用）而不是守卫包。
#
# ★ 2026-10-24 P2-2 再补 3 个包：./internal/extpointledger（扩展点台账）、
#   ./domains/hooks/response（响应拦截链 —— 台账的接线证明在那儿）、
#   与 ./middleware（主链记账 —— 口径门在那儿）。
#   ⇒ 实测（干净环境）：0.4s / 0.9s / 0.9s，绿。
#   ⚠ ★ **admin 包故意不进本快门**：★ 它的测试套件有 **481 个测试文件**且大量
#   ★ 触碰 DB/集成路径（★ 实测 `admin/*_test.go` 里命中 redis/sql.Open/
#   ★ testcontainers/integration 的有数十个）⇒ ⇒ **CI 的 Linux 4 核容器里直接跑
#   ★ 它有外部依赖风险** ⇒ ⇒ 它的门**故意不带** `AUDIT-GUARD` 标记（主要判据放在
#   ★ internal/extpointledger 与 scripts/ursmcheck）。
#   ⚠ ★ **2026-11-09 r1050 轮复核更正**：★ 这条登记原先的理由是「实测 65.8s 逼近
#   ★ `-timeout=120s`」⇒ ★★★ **而快门预算本轮已提到 600s** ⇒ ★★★ **原理由已失效**
#   ★★★ （★ 600s 下 65.8s 有 ~9x 余量 ⇒ **根本不是超时问题了**）⇒ ★★★ **但结论
#   ★★★ 不变、理由换了**：★ **换成「有外部依赖」这条更强、与预算无关的理由** ⇒
#   ★★★ **登记不放进「因预算而排除」那一类**（★ 那类会随预算变化而失效 ⇒ ★★★
#   ★★★ **写下来的理由会过期，而结论被继承** ⇒ ★★★ 这正是「数字/理由不可能过期」
#   ★★★ 的同款问题）。
#   ★ ★ 注：admin 并**不是**没人跑 ⇒ ★ 它登记在 `GUARD_PACKAGES_SLOW`（★ 1800s
#   ★ 预算）⇒ ★ `make guards` 的 `guards-ursmcheck` 会跑它（★ 本轮慢门实测
#   ★ admin **162.700s**）⇒ ★★ **排除的只是「进 CI 快门」，不是「执行」**。
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
# ★★★ 2026-10-22 12h 审计轮：补登 **3 个从未被执行**的守卫包。
#
#   internal/clockguard（R68 轮：判别型下界「已分析、未实测」的余量棘轮门）
#   internal/stopguard （R71 轮：关闭序列永久卡死 ⇒ 僵尸进程）
#   internal/testguard （R87 轮：破坏性腿 × 目标库守卫线）
#
#   这三个包**在磁盘上存在**（最早的已存在数日），却**从未写进 GUARD_PACKAGES**
#   ⇒ ⇒ `make guards` 从不执行它们 ⇒ **CI 从未跑过这三道门**。
#   ⇒ ⇒ 更糟：`scripts/checks/guards-sync.sh`（CI 的第 1 步，无 continue-on-error）
#   因此**一直是红的** ⇒ 整个 audit-guards-ci 专用 job 一直是失败的
#   ⇒ ⇒ 仓库里一直没有任何人真正看到过绿色的审计门。
#   ⇒ ⇒ 这是本轮挖到的最刺眼的一条**流程闭环**断裂：**门写了、没接线，
#      而接线的那道门自己也是红的 ⇒ 没人发现。**
# ★★★ 2026-10-28（R175 轮）★★ 补登 **`./sql/migrations/startup`**。
#
#   背景：★ 该包有 **~160 个迁移契约测试文件**（migration_NNN_test.go 一族），
#   ★ 而它此前**不在** GUARD_PACKAGES / GUARD_PACKAGES_SLOW **任何一栏** ⇒
#   ⇒ `make guards-fast`（★ CI 的 audit-guards job 跑的就是它）**从不执行它** ⇒
#   ⇒ **本轮新增的 914 门（10 道断言）在 CI 上一次都不会跑** ⇒
#   ⇒ ★★ **「一门没人跑的守卫是文档，不是门」**（★ 本文件上方 R69 的判词，★ 原话）。
#
#   ★ 为什么此前一直没人发现：★ 仓内**有**一道门在管这件事
#   （scripts/ursmcheck/audit_guard_registration_test.go，★ 「门写了却没人跑 ⇒ CI 红」）
#   —— ★ 但它的判据是**显式标记**：★ 门文件自己声明 `AUDIT-GUARD:` ⇒
#   ⇒ **它所在的包必须被登记进 GUARD_PACKAGES / GUARD_PACKAGES_SLOW** ⇒
#   ⇒ ★★ 未经声明的门**连「我该被登记」这件事都不知道** ⇒ ⇒ 只能靠**人**发现。
#
#   ★ 本轮处置（两处缺一不可）：★ ① 门文件头加 `AUDIT-GUARD:` 声明；
#   ★ ② 此处补登该包 ⇒ ★★ 让那句承诺**可被机器验证**。
#
#   ★ 成本：★ 本地实测该包 **11.5s** 全绿 ⇒ ★ 在快门 120s/包的预算内 ⇒ ★ **净收益**。
GUARD_PACKAGES := ./internal/sessionid ./internal/rowsguard ./internal/errdiscard ./internal/dbrows ./internal/jsoncol ./internal/paramguard ./internal/sqlguard ./internal/sqlreadguard ./internal/metricguard ./internal/partguard ./internal/routeguard ./internal/ingressguard ./internal/healthstateguard ./internal/billguard ./internal/jsonbguard ./internal/clockguard ./internal/stopguard ./internal/testguard ./sql/schema ./sql/migrations/startup ./internal/rewrite ./internal/routeorder ./internal/requestobserver ./domains/credential ./domains/authentication ./internal/extpointledger ./domains/hooks/response ./middleware

# ★★★ 2026-10-22 12h 审计轮：./scripts/ursmcheck 此前**不在任何强制执行路径**上。
#
#   它有一批门文件（CJK 变量名棘轮、R75 巡检基线快路门、pipefail 台账门、
#   cron 注册门、投递门、变更点门…）⇒ 这些门在 CI 上**一次都没跑过**。
#   ⇒ 一门没人跑的守卫是文档，不是门（这正是本文件上方 R69 的判词）。
#   ⇒ 单独一个 target：它比主门慢一个量级（实测 ~5~6 分钟 vs 主门 120s 上限），
#     挂在 `guards` 的依赖里 ⇒ CI 现有的 `make guards` 一步自动覆盖它，
#     不需要改 workflow。
#   ⇒ 登记进 GUARD_PACKAGES_SLOW ⇒ scripts/checks/guards-sync.sh 的双向检查
#     同样覆盖它（漏登记/登记错了会红）。
# ★★ 2026-10-30 r1050 轮：**「94 个」是 2026-10-22 那轮的历史读数**（★ 当时确实
#   是 94 个）⇒ ★★ **它早已不是当前值**：★ 当时实测 `scripts/ursmcheck/*.go`
#   = 165 个（★ 磁盘与 `git ls-files` 逐项一致 ⇒ 口径确定）⇒ ★★ **2026-11-09 再量
#   ★★ 已是 180 个** ⇒ ★★★ **⇒ 这条注释就是「写死的读数会漂」的活样本** ⇒
#   ★★★ **★ 故本文件里不再复述当前值**（★ 当前值由 `scripts/checks/guards-sync.sh`
#   ★★★ 的 `find` **实时数**给出 ⇒ ★★★ **不可能过期** ⇒ ★★ 每次要数就跑它）。
#   ⇒ ★★★ 2026-11-09 补（并行会话各自独立收敛到同一结论 ⇒ 记此以免后人重复推演）：
#     ★ 上游把「165」手改成「166」⇒ ★★ 而当时的真值是 **180** ⇒ ★★★ **写死的数字必漂**
#     ★★★ （★ 手改的那个数**当场就是错的** ⇒ ★★ 比不改更坏：★ 它看起来是「核对过的」）。
#     ★ 本仓另有一路独立做法：★ **在 Makefile 内部自己实时取**（文件头的
#     ★ `URSMCHECK_GATE_COUNT` ⇒ `$(shell find …)`）⇒ ★★ 于是 `make help` 打印的
#     ★★ 门文件数**永远是活的**（★ 2026-11-09 实测：加删一个门文件 ⇒ 181/180 互变）
#     ★★ ⇒ ★★ 两个实时源（guards-sync.sh 的 find + 本文件的 $(shell)）**口径同源**
#     ★★ ⇒ ★★ 互相印证。
#   ⇒ ★★ **同一个 165 与 r1049 轮的「165」是不同集合**：★ 那 165 指
#   ★ 「调用编译型 go 子命令的 `.sh`/`.py` 脚本数」，★ 这 165 指「门文件数」
#   ⇒ ★★ **撞号不是同一件事** ⇒ ★★ 引用时必须写清口径，否则会互串。
# ★★★ 2026-10-24 magpie 对齐轮 §3.17：**补登记 `./admin`**（P2-2 薄壳门所在包）。
#
#   为什么此前不登记：★ 原判断是「admin 套件 65.8s ⇒ 逼近快门 120s 上限 ⇒
#   拖进来会把 CI 拖到超时」。那个顾虑**仍然成立** ⇒ 所以进**慢门**（900s）。
#
#   ★ 订正（原审计档 §3.16.3 的结论，勿回退）：
#   原写「⇒ ★★ 结构性缺口：`admin` 包的测试**没有任何 CI 路径** ⇒ 它的红没人看见」
#   ——★ **错**。实测：官方门 `verify.sh` 跑 `go test ./... -count=1 -timeout=900s`
#   ⇒ **`./admin` 在其中** ⇒ 它的红**会被官方门看见**（实测：红过的那条
#   `TestTurnsSessionSQLHasNoFormatArtifacts` 在 `-short` 下同样复现）。
#   ⇒ ★ 真正缺的是「**审计守卫体系整体在 CI 上空转**」⇒ 已由
#   `.workflow/main-verify.yml` 接 `make guards-fast` 修复。
#   ⇒ 本次补登记的**真实收益** = 让 admin 在**审计守卫这条专用路径**上也有位置
#   （登记门的 G2 判据要求：带 AUDIT-GUARD 标记的门文件，其包必须被登记）。
#
#   ⚠ 实测：4 核 Linux 容器内 `go test ./admin` ≈ 66s（本地 macOS 64.6s）
#   ⇒ ⇒ 它**不能**进快门（120s/包上限，余量太悬）⇒ 进慢门是对的。
GUARD_PACKAGES_SLOW := ./scripts/ursmcheck ./admin

.PHONY: guards
guards: guards-installer guards-ursmcheck guards-shell guards-deploy guards-deploy-state-machine guards-audit-ledger guards-gate-scripts guards-baseline-copies ## 运行全部审计守卫（快速、无外部依赖；含 installer 跨模块门 + ursmcheck 全量门 + shell 全角变量门 + 切流锁两门 + 切流状态机慢门 + 跨文档登记册门 + 门脚本接线门 + 基线副本一致性门）
	$(GO) test $(GUARD_PACKAGES) -count=1 -timeout=600s

# ★★ 2026-11-09 r1050 轮复核：**快门预算由 120s 提到 600s**（★ `guards` 与
#   ★ `guards-fast` **两个 target 同档** ⇒ ★★ CI 跑的那条也一起提）。
#   ★ 起因（★ 实测，非推断）：★ 复核跑 `make guards-fast` ⇒ `internal/sqlguard`
#   ★★ **FAIL 120.029s**，日志里是 `panic: test timed out after 2m0s` ⇒ ★★ **是腰斩、
#   ★★ 不是判据红**（★ 隔离重跑 `-timeout=600s` ⇒ **ok 170.156s** ⇒ ★★ 绿但慢）。
#   ★★★ **而 170.156s > 120s ⇒ ★★★ 它本来就跑不完自己的预算** ⇒ ★★★ **任何稍慢的机器
#   ★★★ 上 CI 主门都会腰斩** ⇒ ⇒ ★★ **这不是「并发噪声」，是预算失配**。
#   ★★ 而腰斩**不是「一个红包」**（★ r1044 轮 §5.2.2 同款机理）：★ `testing` 的超时是
#   ★★ **alarm goroutine 里的 panic** ⇒ ★★ 其它 goroutine 的 `defer` 不执行 ⇒ ★★ 而
#   ★★ `internal/sqlguard/sql_literal_guard_selftest_test.go` **确实有写仓内文件的操作**
#   ★★ ⇒ ⇒ ★★ **被跟踪的台账可能静默留空** ⇒ ⇒ ★★ 预算必须给足。
#
#   ⇒ ★★★★ **★★ 预算必须按「在门里跑」的读数定，不是按「隔离跑」的读数定 ★★**
#   ★★ 这是本轮最贵的一条教训（★ 三次读数给出三个不同的数）：
#   ★★   ① 隔离跑（低负载）：**170.156s**
#   ★★   ② 隔离跑（有负载档，★ 用 `/usr/bin/time -l` 取 CPU）：**189.16s**
#   ★★      ⇒ 而 **CPU 仅 44.15s**（11.34 user + 32.81 sys）⇒ ★★★★ **慢在 I/O 与
#   ★★★      调度争用，不在 CPU** ⇒ ★★★ **墙钟读数受负载支配，CPU 读数不受**
#   ★★   ③ **在门里跑**（★ 29 个包并行，★ `go test` 默认 GOMAXPROCS 并行度，
#   ★★      ★ 同一台机器、同一外部负载档）：**268.823s** ← ★★★★ **门内的真实成本**
#   ★★★ ⇒ ★★ ① 与 ③ 差 **1.58x** ⇒ ★★★★ **⇒ 按隔离读数定预算 ⇒ 门里必腰斩**
#   ★★★★ ⇒ ★★ 取 **600s**（★ 对 268.823s 留 **~2.23x** 余量；★ 对门内次慢的
#   ★★★ `sql/migrations/startup` 237.521s 留 ~2.5x）。
#   ★★★★ **为什么不能只按「本机实测 + 小余量」定**：★ CI 是 **4 核 Linux 容器**
#   ★★★★ （★ 本机 10 核 + 外部负载 250~345）⇒ ★★★ **CI 的单核速度、磁盘与并行度
#   ★★★★ 都可能更慢** ⇒ ★★ **在没有 CI 复测读数之前，预算必须按上界定**。
#   ★★ 慢用例是**分散**的（★ sqlguard 本轮 per-test 实测：64.40s + 32.35s +
#   ★★ 30.27s + 27.05s + 21.89s + 8.41s，其余 < 3s）⇒ ★★ **拆用例不如提预算**
#   ★★ （★ 拆了还要逐个重估，风险更高；★ 且这些是**读仓内文件**的门，拆了也快不了）。
#   ★★ 门内其余各包实测：★ partguard 160.477s · sql/schema 139.837s ·
#   ★★ billguard 113.030s · rowsguard 94.549s · domains/credential 77.618s ⇒ ★★ 600s
#   ★★ 对它们都有 **≥2.2x** 余量。
#   ⚠ 提高的仍只是**每包**预算，不是总时长：各包并行执行，★ 总墙钟由最慢的包决定
#   ⚠ ⇒ ★★ **不会**让 CI 墙钟翻倍（★ 实测：改预算前后 CI 侧墙钟由「脚本门主导」
#   ⚠ 决定，★ Go 段本来就由最慢的 sqlguard 决定）。
#   ⚠ 这**不是**「把阈值调大以让检测变松」：★ 超时是 harness 的**杀开关**，★ ★ 调大它
#   ★ ★ 不会让任何一道门从红变绿 ⇒ ★★ 它只会让一个**健康但慢**的包不被腰斩。
#   ⚠ 反过来**也不能小**：★ 腰斩会把「绿」变成「红」，★ 且**破坏被跟踪台账的落盘**
#   ⚠ ⇒ ★★ 一个**假红**比一个**慢门**贵得多。

# ── 切流锁契约门 + 跨仓对拍门 ───────────────────────────────────────────────
#
# ★ 2026-10-28（r1039 后续轮 · 第三轮）：★ §507 的教训「**门没有调用方 = 声明
#   不是门**」—— tests/deploy_switch_lock_contract_test.sh（20 条不变量）与
#   tests/deploy_switch_lock_parity_test.sh（跨仓对拍）自 2026-10-27 起
#   **没有任何 Make / CI 调用方** ⇒ ⇒ 只有「人恰好记得跑」时它们才跑。
#   ⇒ ⇒ 这里给它们一个调用方（★ 前两轮的判据成果由此才真正【承重】）。
# ★ 缺件时的行为（★ 两条门都遵守「本次没跑」纪律，**不**伪装成通过）：
#   · 跨仓对拍门缺仓外共享部署库 ⇒ 明确 SKIP（不输出 ALL PASS）；
#   · 契约门的 flock 形态臂缺 python3（真语义假 flock 需要 fcntl）⇒ 明确 SKIP 那一段。
.PHONY: guards-deploy
guards-deploy: ## 切流锁契约门 + 跨仓对拍门（缺件时明确 SKIP，不伪装成通过）
	@bash tests/deploy_switch_lock_contract_test.sh
	@bash tests/deploy_switch_lock_parity_test.sh
	@bash tests/deploy_test_gate_contract_test.sh
	@bash tests/deploy_force_lock_recovery_test.sh

# ── 门脚本接线门（把「从未被运行过」的门接进强制路径）──────────────────
#
# ★ 2026-11-02 第二十四轮：★ 实测全仓门脚本（tests/*.sh + scripts/checks/*.sh +
#   scripts/guards/*.sh）**接线前 56** 个，其中：
#     · 32 个有**可执行**调用方；
#     · 20 个**只在文档里被提及**（人工触发）⇒ 按「门没有调用方 = 声明不是门」，
#       它们不是门；
#     · ★ **4 个连文档都没有** ⇒ 完全隐形 ⇒ 其中两条**已经坏掉/红着**：
#         · tests/session_storage_audit_gate_test.sh 依赖 ripgrep
#           （CI 精简镜像没有）⇒ verify.sh **一直是红的**；
#         · tests/deploy_seamless_state_machine_test.sh 间接需要 bash ≥ 4
#           ⇒ macOS 自带 bash 3.2 下 cryptic 崩。
# ⇒ 本轮把其中 **13 条**接进强制路径：
#   · **12 条快的**（实测单条 0–14s、零外部依赖）⇒ verify.sh **与**本 target；
#   · **1 条慢的**（tests/deploy_seamless_state_machine_test.sh，实测 **≈270s**）
#     ⇒ 单列一个 target（见下），进 `make guards` 与 verify.sh，
#     ★ **不进 guards-fast** ⇒ 理由见该 target 的注释。
# ⇒ 余下未接线的**逐条登记**在 tests/gate_scripts_caller_registry.tsv
#   （含原因）⇒ 由 tests/gate_scripts_caller_registry_test.sh 守住：
#   新增的门若没有调用方 ⇒ 门红；已接线的必须从登记表里删掉 ⇒ 棘轮。
# ⇒ ★ 接线后再测：57 个门（+1 = 登记册门自己）⇒ 44 个有调用方、13 个没有
#   ⇒ 这 13 个**全部已登记** ⇒ **0 个隐形**。
#
# ★ 2026-11-04 审计轮（F-15）：★ 又接进 **1 条**快门
#   （tests/shared_lib_resolution_contract_test.sh）⇒ 该 target 的快门数 12 → **13**。
#   ★ 起因：★ `_resolve_aian_deploy_lib` 此前**没有任何机器覆盖**
#   （兄弟解析器 deploy-lib-resolve.sh 有 .verify-deploy-lib-resolution.sh）
#   ⇒ ★ 护栏不对称；且旧实现对**显式** AIAN_DEPLOY_LIB **不校验** ⇒ 无效值要到
#   source prereqs 才炸出 `No such file or directory`（rc=1），
#   而兄弟解析器对同一输入给 rc=64 + 可执行提示 ⇒ ★ 两条解析路径语义不一致。
#
# ★ 2026-11-04 审计轮（F-18 / F-22）：★ 再接进 **1 条**快门
#   （tests/scan_secrets_enumeration_contract_test.sh）⇒ 该 target 的快门数 13 → **14**。
#   ★ 起因：★ `scripts/scan-secrets.sh` 的 `--tracked-only` 枚举分支有【★ 覆盖洞 ★】——
#     ★ ① `git ls-files` 吃 `core.quotePath=true` ⇒ 非 ASCII 文件名被转义后丢弃（F-18）；
#     ★ ② `xargs` 按空白切分 ⇒ 含空格的文件名被切成不存在的路径（F-22）；
#   ⇒ ★ 两者都是【★ 静默少读、★ 不报错、★ 不计数、★ 不让任何门变红 ★】★
#   ⇒ ★ 而这条枚举分支正是【★ 两道公开仓闸门 ★】（`.githooks/pre-push` ＋
#     ★ `scripts/sync-to-github.sh`）★ 用的那一条 ⇒ ⇒ ★ 它失明 = 公开仓闸门失明 ★★。
# ★ 2026-11-05（211 蓝绿并发事故轮 + 同步效率轮）：★ 再接进 **2 条**快门
#   （tests/deploy_target_lock_concurrency_test.sh、tests/deploy_sync_prune_contract_test.sh）
#   ⇒ 门脚本门 14 → **16** 条。
#   · 实测耗时 **3s / 21s**（合计 ≈24s），★ **bash 3.2 可跑**（无 bash4 特性）、
#     零外部依赖：目标锁门用 **PATH 垫片**把 ssh/scp 换成本机执行（真引擎、真远端命令
#     形态，但不碰真目标机）；剪枝门执行**引擎里那条真实的 find + tar 命令**。
#   · ★ **接线是硬要求，不是形式**：新增门若没有可执行调用方 ⇒
#     tests/gate_scripts_caller_registry_test.sh（登记册门）当场报红
#     ⇒ 「门没有调用方 = 声明不是门」（LESSONS §507）⇒ ★ **本仓的红有时是「门本身红了」，
#     有时是「门没接线」** —— 两者的修法完全不同，别混。
#   · ★★ **位置有意排在 tests/deploy_local_contract_test.sh 之前**：
#     `make` 遇红即中止 ⇒ 排在后面的门在**前面那条红着**时**根本跑不到**
#     ⇒ ★ 「接线了却跑不到」＝ **门自称有牙实则没牙**（比没接线更坏：它让人以为被守住了）。
#     ⇒ ★ 排位本身就是门的一部分：**先保证每条门都真的被执行过一次**。
#     （本机实测：deploy_local_contract_test 因 agent PATH 上的 `rm` 是可恢复删除
#      包装器、拒绝 /var/folders 沙箱外路径而红 ⇒ 换真 /bin/rm 即绿 ⇒ **量具失真**，
#      非仓内缺陷；详见 LESSONS §633。）
#
# ★ 2026-11-05（245/154 部署断链轮）：★ 再接进 **1 条**快门
#   （tests/envs_resolution_contract_test.sh）⇒ 该 target 的快门数 16 → **17**。
#   ★ 起因：★ 工作区 2026-11-04 重组（根仓 85faac2a）把 envs 仓落到
#     ai-native-tools/deploy/envs ⇒ 245/154 部署脚本里的**两候选链**
#     （ai-native-workspace/envs → ai-native-tools/envs）**两个都落空**
#     ⇒ 每次部署第一步就退出（`envs SSOT loader not found: …/ai-native-workspace/
#       envs/loader.sh` —— ★ 一条本机**从来不存在**的死路径 ⇒ 看起来像「envs 丢了」★
#       ⇒ 实际是「默认值没跟着迁移改」）⇒ ⇒ 解析收口到 scripts/envs-resolve.sh，
#     并给它配一道**行为门**（★ 夹具由**实现自己的候选链**推导 ⇒ 布局改名不假红 ★；
#     ★ 变异臂已实测：旧实现 14 红 / 显式值不校验 7 红 / 文案漂移 1 红 ★）。
#   ★ ★ 计数订正：★ 本 target 的 @bash 行实测 **19 条快门 + 1 条登记册门**（=20 行）
#     ⇒ ★ 上面那句「16 → 17」只记**本轮增量**；★ 历史计数（12→13→14→16）漏记了
#     后加的门 ⇒ ★ **以行数为准** ★（计数类结论：口径必须和数字一起发布）。
#   ★ ★ **排位**：★ 本轮这条门有意排在 **tests/deploy_local_contract_test.sh 之前**
#     ⇒ `make` 遇红即中止 ⇒ 排在后面的门在前面红着时**根本跑不到**
#     ⇒ ★「接线了却跑不到」＝ 门自称有牙实则没牙（比没接线更坏）★。
#     （本机实测：deploy_local_contract_test 在 agent PATH 的 `rm` 包装器下红、
#       换真 /bin/rm 即绿 ⇒ **量具失真**、非仓内缺陷 ⇒ §633。）
#
# ★ 2026-11-05（245/154 部署断链轮 · 第二处）：★ 再接进 **1 条**快门
#   （tests/migration_retired_marker_contract_test.sh）⇒ 该 target 的快门数 17 → **18**。
#   ★ 起因：★ 部署推进到「切换前 pending 迁移」这步时**中止**：
#     946_sales_quote_items.sql → ERROR: relation "public.sales_quote_versions" does not exist
#     ⇒ 根因＝共享部署库的**退役判据**是「前 15 行出现 superseded/deprecated 就跳过」
#       ⇒ 944/945 的**表头业务词汇**把这两条迁移**静默丢弃** ⇒ 两张表从未建出来。
#   ★ 修法在共享 SSOT（deploy-lib/db-changelog.sh）：退役判据收口为【★ 单点真源 ★】
#     helper（**标记词必须是注释行的第一个词** ⇒ 区分「标记」与「提及」）
#     ＋ 跳过**可见**（打日志）＋ 本仓配一道行为门（用**共享库的真实现**做判据 ⇒ 有牙齿）。
#
# ★ 2026-11-09（第十三轮 · 扫描器正则口径确定性轮）：★ 再接进 **1 条**快门
#   （tests/scan_secrets_regex_determinism_test.sh）⇒ 该 target 的快门数 19 → **20**。
#   ★ 起因：★ 同一棵扫描器在【★ 不同平台 ＋ 不同 locale ★】下给出不同读数 ★
#     ⇒ ★ 实测（★ 树态 `2f26e91eb` ★、★ 扫描面 **18211 个文件** ★、★ find 枚举 ★）：
#     ★ macOS(C.UTF-8) **180** 条 ／ macOS(C) **185** 条 ／
#     ★ glibc(C.UTF-8) **1325** 条 ／ glibc(POSIX) **1332** 条
#     ⇒ ★ 平台差 **1145 条、★ 100% 落在 INTERNAL_IP 一族 ★】⇒ ★ 根因是【★ libc 正则引擎 ★】
#     ★ 而 locale 差只有 **5～7 条** ★。
#   ★ 本门钉三条不变量：
#     ① 规则里【★ 不得出现不可移植的词边界 ★】（★ `\b` 在 BSD libc 的 regcomp 里
#       【不是词边界】⇒ ★ 那让 INTERNAL_IP 三条规则在 macOS 上【★ 整族失效 ★】★）
#     ｜② 发现阶段（★ `grep -niE` ★）与归因阶段（★ `[[ =~ ]]` ★）必须【★ 同口径 ★】
#       ⇒ ★ 否则大写形态的凭据【★ 候选命中却被静默丢弃 ★】⇒ ★ BLOCK 级假阴性 ★
#     ｜③ 词边界语义不得被"过度修法"抹掉（★ 粘连形态必须不命中 ★）。
#   ★ 变异臂已实测：★ 旧实现 **11 红** ／ 撤掉大小写折叠 **2 红** ／ 删掉词边界 **2 红** ★
#     ⇒ ★ 每条不变量都有牙 ★。
#   ★ 实测耗时 ≈2s、★ **bash 3.2 可跑**（★ 门内自己找 bash ≥ 4 去调扫描器 ★）、
#     ★ 零外部依赖、★ 全部在 mktemp 里 ⇒ 可进 CI 快门 ★。
# ★ 2026-11-09（第五十轮续记之三 · 白名单豁免面）：★ 再接进 **1 条**快门
#   （tests/scan_secrets_placeholder_privilege_test.sh）⇒ 该 target 的快门数 20 → **21**。
#   ★ 起因：★ 扫描器的白名单是【★ 按值豁免 ★】的 ⇒ ★ 一旦某批占位符形态的口令值
#     进了白名单、★ 【★ 同一个值哪天出现在生产代码里、★ 也一样会被豁免 ★】
#     ⇒ ⇒ ★ 那一类问题会【★ 永久变成不可见 ★】★★ ★
#   ★ 而【★ 白名单本身还没有落地 ★】⇒ ★ 那批值【★ 现在就已经在生产代码路径上 ★】零命中
#     ⇒ ★ 一旦落地、★ 这条「★ 当时是零 ★」的读数【★ 就不再被任何人守着 ★】
#   ★ 所以本门【★ 不依赖扫描器的白名单 ★】、★ 而是【★ 自己独立扫一遍 ★】
#     ⇒ ★ 白名单落地之后、★ 它【★ 依然有牙 ★】★★ ★
#   ★ 门的判据面【★ 直接取自扫描器的规则表 ★】（DB_CONNSTRING ／ CRED_ASSIGN）
#     ⇒ ★ 零漂移 ⇒ ★ 【★ 门不会比扫描器更宽 ★】★★ ★
#   ★ 三条量具自校准：★ ① 【★ 生产代码路径文件数下限 ★】（★ 防「清单空了」假绿 ★）
#     ｜② 【★ 判别力夹具走【★ 同一条管线 ★】 ★】（★ 走另一条管线则门坏掉而门照样绿 ★）
#     ｜③ 【★ 门自身不得成为处置面的一条命中 ★】（★ 形态一律运行时拼出 ★）
#   ★ ★ ★ 2026-11-09（第五十轮续记之四 · 豁免面泛化）：★ 本门【★ 新增三条断言 ★】★★ ★
#     ｜④ 【★ 白名单在生产代码路径上的【★ 凭据族豁免面 ★】== 已知清单 ★】（★ 19 条键、
#     ★   **棘轮** ★）⇒ ★ 白名单一改、★ 豁免面就【★ 静默地 ★】变掉 ★ ★ ★
#     ｜⑤ 【★ 豁免面里【★ 凭据位上是字面量 ★】的【★ 只许那已知的一条 ★】★★ ★
#       ⇒ ★ ★ 而那已知的一条【★ 正是那处生产代码硬编码 DSN ★】★★ ★
#       ⇒ ★ ★ 它【★ 今天对扫描器完全不可见 ★】★★ ★（★ 因为整行被白名单豁免 ★）
#     ｜⑥ 【★ 白名单在生产代码路径上的【★ 地址族豁免面 ★】== 已知清单 ★】
#       （★ 20 个键、★ **棘轮** ★）⇒ ★ ★ 而它的键【★ 刻意不用地址值 ★】★★ ★
#       ⇒ ★ ★ 否则【★ 内网地址一改、★ 门就红 ★】⇒ ★ ★ 而【★ 地址变更【★ 是日常运维动作 ★】★★ ★
#       ⇒ ★ ★ ⇒ ★ ★ 而【★ 它守的是【★ 读数失真 ★】而不是【★ 凭据泄露 ★】★★ ★
#     ⇒ ★ ★ 而【★ 这三条对【★ 将来新增的任何白名单条目 ★】都有效 ★】★★ ★
#   ★ 实测耗时 ≈38s、★ **bash 3.2 可跑**、★ 零外部依赖 ⇒ 可进 CI 快门 ★。
# ★ ★ ★ 2026-11-09（第五十轮续记之五 · 枚举面契约）：★ 再接进 **1 条**快门
#   （tests/git_enumeration_quote_path_contract_test.sh）⇒ 该 target 的快门数 21 → **22**。
#   ★ 起因：★ F-18 修的是【★ 扫描器自己 ★】那一处枚举点、★ 而【★ 别的消费点 ★】至今【★ 零覆盖 ★】
#     ⇒ ★ ★ 而【★ `git ls-files` 默认 `core.quotePath=true` ★】⇒ ★ 非 ASCII 路径被转义成八进制
#       ＋ 外层双引号 ⇒ ★ 而【★ 下游的 `^前缀` 锚点【★ 会落空 ★】★★ ★
#     ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 这一类问题【★ 不报错、★ 不计数、★ 不让任何门变红 ★】★★ ★**
#   ★ 本门钉的是【★ 全仓枚举面消费点 ★】：
#     ｜① 消费点【★ 必须被清点出来 ★】（★ 剔注释与字符串常量 ★）
#     ｜② 不带 `-z` 的消费点【★ 必须逐条登记在豁免清单里 ★】（★ 双向棘轮 ★）
#     ｜③ 每条豁免【★ 必须用差分探针证明「★ 转义不改变它的判定 ★】★★ ★
#     ｜④ 门自己有牙（★ 非 ASCII 探针 ＋ 前缀锚点探针 ★）
#   ★ 实测：★ 清点 35 处（★ 带 -z 27 ／ 不带 -z 8 ★）｜★ 豁免 8 条【★ 逐条差分成立 ★】｜★ **PASS=14 FAIL=0** ★
# ★ ★ ★ 2026-11-09（第五十轮续记之六 · metricguard 既有红修复）：★ 再接进 **1 条**快门
#   （tests/mcp_metrics_scrape_wiring_test.sh）⇒ 该 target 的快门数 22 → **23**。
#   ★ 起因：★ internal/metricguard 那道门判的是「生产代码里有没有记录调用」（令牌级
#     「标识符.记录方法」形态）⇒ ★ 它【★ 看不见「记录调用挂在哪个抓取面上」★★ ★
#     ⇒ ★ 而 mcp_gateway_uptime_seconds【★ 只有抓取时刻才有意义 ★】★★ ★
#     ⇒ ★ 记录调用挂在包里、而 /metrics 抓取面没接 ⇒ 指标恒为 ABSENT ★★
#   ★ ⇒ ★ ★ 而【★ 这两件事之间那道缝、★ 没有任何既有门覆盖 ★】★★★ ★
#   ★ 本门钉的是【★ 暴露 mcp_gateway_* 指标族的 /metrics 抓取面 ★】：
#     ｜① 「服务 mcp 指标」的二进制集合【★ 必须被清点出来 ★】★（★ 双向棘轮 ★）
#     ｜② 每个这样的二进制【★ 必须有 ≥1 个 /metrics 抓取面 ★】、
#       【★ 且抓取面数 ≤ ScrapeHandler 包装调用数 ★】★★ ★
#     ｜③ 反向不变量：★ ScrapeHandler 只许出现在 ① 的集合里 ★
#     ｜④ 门自己有牙（★ 合成树差分探针：★ 已接线臂判绿、未接线臂判红 ★）★★ ★
#   ★ ★ ★ 而【★ 这道门【★ 自己的差分探针 ★】在第一版就抓出了【★ 它自己抽取器的一个 bug ★】★★ ★
#     ★ ⇒ ★ BSD grep（macOS 自带）与 GNU grep 在【★ ERE 的转义括号 ★】上不一致
#       ⇒ ★ 而抽取面【★ 看着有、★ 其实一条都抽不到 ★】★★ ★
#     ⇒ ★ ⇒ ★ 路径判定改成【★ 先抽路由字面量、★ 再精确比对 ★】★★ ★
#   ★ 实测：★ **PASS=5 FAIL=0** ★｜★ 零外部依赖 ⇒ 可进 CI 快门 ★。
.PHONY: guards-gate-scripts
guards-gate-scripts: ## 门脚本接线门（24 条快门 + 登记册门，防新增隐形门）
	@bash tests/audit_154_wrapper_test.sh
	@bash tests/credential_script_contract_test.sh
	@bash tests/deploy_seamless_mobile_surface_test.sh
	@bash tests/selfcheck_diagnostics_contract_test.sh
	@bash tests/bump_version_collision_test.sh
	@bash tests/deploy_blue_green_contract_test.sh
	@bash tests/deploy_target_lock_concurrency_test.sh
	@bash tests/deploy_sync_prune_contract_test.sh
	@bash tests/deploy_lock_test.sh
	@bash tests/deploy_port_rotation_test.sh
	@bash tests/deploy_promotion_test.sh
	@bash tests/deploy_readiness_contract_test.sh
	@bash tests/deploy_sops_test.sh
	@bash tests/envs_resolution_contract_test.sh
	@bash tests/migration_retired_marker_contract_test.sh
	@bash scripts/deploy-local-lib-project_env_test.sh
	@bash tests/deploy_local_contract_test.sh
	@bash tests/shared_lib_resolution_contract_test.sh
	@bash tests/deploy_lib_candidate_chain_single_source_test.sh
	@bash tests/scan_secrets_enumeration_contract_test.sh
	@bash tests/scan_secrets_whitelist_regex_portability_test.sh
	@bash tests/scan_secrets_regex_determinism_test.sh
	@bash tests/scan_secrets_placeholder_privilege_test.sh
	@bash tests/git_enumeration_quote_path_contract_test.sh
	@bash tests/mcp_metrics_scrape_wiring_test.sh
	@bash tests/gate_scripts_caller_registry_test.sh

# ── 切流状态机门（★ 慢门：实测 ≈270s ⇒ 不进 CI 快门）─────────────────────
#
# ★ 2026-11-02 第二十四轮：这条门是本轮抓到的**第二条「从未被运行过」的门**。
#   它自己不用 bash4 特性，但 source 的 tests/lib/fake-remote-host.sh 用
#   `declare -A` ⇒ **间接**需要 bash ≥ 4 ⇒ macOS 自带 bash 3.2 下 cryptic 崩
#   （`declare: -A: invalid option`）⇒ ★ 加上「它没有任何调用方」⇒ 坏了没人知道。
# ⇒ 本轮给它补了**可执行守卫**（读 BASH_VERSINFO + 明确指引 + exit 64）
#   ⇒ macOS 上从 cryptic 崩变成可执行地拒绝。
#
# ★ ★ 为什么**单独成一个 target**，且**不进 guards-fast**：
#   实测（macOS，含 bash5 包装器）**≈270s**（265s / 272s 两次）⇒ 而 CI 快门的预算是 **116s**
#   （见 .workflow/main-verify.yml 的实测注释）⇒ ⇒ 把它塞进 guards-fast
#   会把 CI 预算撑到 **3 倍以上** ⇒ ⇒ **不进快门**。
#   ⇒ 这与仓里既有的慢门处理**完全一致**（guards-ursmcheck 实测 1122s，
#     同样只进 `make guards` 与 verify.sh，不进 guards-fast）。
#   ⇒ ★ 它**有**可执行调用方（make guards / verify.sh）⇒ 按本仓纪律
#     「门没有调用方 = 声明不是门」，它**是门**，不是文件。
#   ⇒ 包装器 scripts/run-bash4-gate.sh：找得到 bash ≥ 4 ⇒ 真跑；
#     找不到 ⇒ **显式 SKIP**（rc=0，不伪装成通过）。
.PHONY: guards-deploy-state-machine
guards-deploy-state-machine: ## 切流状态机门（慢门，实测 ≈270s ⇒ 只进 make guards 与 verify.sh）
	@bash scripts/run-bash4-gate.sh tests/deploy_seamless_state_machine_test.sh

# ── 跨文档登记册一致性门 ─────────────────────────────────────────────────
#
# ★ 2026-11-02 第二十二轮：仓里有**两份**审计档，各带一份**登记册**：
#   · docs/audit/2026-11-02-上游4xx被记成节点不健康-下一轮修复项.md（主档）
#   · docs/12小时内修订审计-2026-11-02-0350.md（兄弟档）
#   ⇒ **一份更新了、另一份没更新** = **静默分叉**：后来者照着旧登记册会把
#     **已闭合**的项重新开一遍（而那项的「重新实现」往往会把后来者引回
#     被否过的方向）⇒ ⇒ 两份登记册必须在**状态**上互相对拍。
# ★ 本门判据：主档标闭合 ⇒ 兄弟档不得仍写「❌ 登记」、该行必须带闭合标记、
#   该行不得整行消失；兄弟档 §六 每项必须带状态标记、头部声明项数不得与
#   §六 脱节、条目数不得少于棘轮下限；「仍开」集合两档必须**双向等价**。
# ★ **有调用方**：挂在 `guards` 与 `guards-fast` 两处 —— `guards-fast` 是
#   codeup Flow（.workflow/main-verify.yml）里**唯一**被调用的守卫入口 ⇒
#   本门因此在真正在跑的 CI 里会被执行（§507「门没有调用方 = 声明不是门」）。
#   实测约 0.1s、零外部依赖、bash 3.2 可跑（无 bash4 特性）。
.PHONY: guards-audit-ledger
guards-audit-ledger: ## 跨文档登记册一致性门（两份审计档的登记项状态必须一致）
	@bash tests/audit_ledger_consistency_test.sh

# ── 基线副本一致性门 ───────────────────────────────────────────────────────
#
# ★ 2026-11-09（第 0 批零风险清理 · 项2）：基线 SQL（00-prereqs / 01-schema /
#   02-seed）在仓内各有**三处**副本：
#     sql/schema/（SSOT 正典）/ installer/.../embeddata/（新装库 //go:embed）/
#     deploy/sql/schemas/baseline/（随包分发基线）
#   ⇒ 9 个实体必须**逐字节相同**，否则新装库与存量库的形状分叉。
#
# ★ 为什么**又**有一道 shell 门（Go 门已在 ./sql/schema 里，见
#   sql/schema/baseline_copies_identity_test.go）：
#   ① Go 门**只报红不修** ⇒ ★ 人需要一条**能修**的命令 ⇒ 本脚本的 --apply
#      就是那条命令（从 SSOT 同步两份副本）⇒ ⇒ 「手改三处」这个动作消失了；
#   ② Go 门住在 GUARD_PACKAGES（快门），本门**额外**把同一不变量落成
#      「一条可执行的、任何人可跑的」检查（★ 排查时不用先会 go test）。
#
# ★ 实测：9 次 cmp（合计 ~1.2MB）⇒ **毫秒级**、零外部依赖、bash 3.2 可跑
#   ⇒ ⇒ **进 guards-fast**（codeup Flow 的唯一守卫入口 ⇒ 真正在 CI 上跑）。
#
# ★ **有调用方**（make guards / make guards-fast）⇒ 按本仓纪律
#   「门没有调用方 = 声明不是门」，它**是门**，不是文件。
.PHONY: guards-baseline-copies
guards-baseline-copies: ## 基线副本一致性门（三份基线 × 三处副本，逐字节；漂移即红，--apply 可修）
	@bash scripts/checks/sync-baseline-copies.sh --check

.PHONY: guards-win-baseline
guards-win-baseline: ## Windows 全量测试基线门（bash scripts/guards/check-win-test-baseline.sh <日志>；无参=自跑全量，约 10 分钟）
	@bash scripts/guards/check-win-test-baseline.sh $(WIN_TEST_LOG)

# ★★★ 2026-10-24 magpie 对齐轮 §3.17：**在跑的 CI 载体专用 target**。
#
#   起因（★ 本轮实测推翻了自己审计档 §3.16.3 的结论）：
#     ① 本仓远端是 codeup.aliyun.com（`git remote -v` 实测）⇒ **`.github/workflows/`
#        里的 9 个 workflow 在本仓从来不在任何在跑的 CI 里**。
#     ② ⇒ `make guards` 的**唯一**调用方（`.github/workflows/audit-guards-ci.yml`）
#        **不在执行路径上** ⇒ ★★ **整个审计守卫体系在 CI 上是空转的**。
#     ③ 而 `guards-sync.sh` 的「专用门还在」判据只 grep `.github/workflows/`
#        ⇒ 它证明的是「**文件还在**」，不是「**门在跑**」⇒ ★ 自证型门的第四形态。
#     ④ ⇒ 本仓**真正在跑的**流水线载体是 codeup Flow（`.workflow/main-verify.yml`）。
#
#   本 target 的定位：**只跑 CI 预算内的部分**。
#     · 快门（GUARD_PACKAGES）—— ★ **2026-10 月的 4 核 Linux 读数**：116s 墙钟、
#       最慢单包 14.3s（★ 该读数**已过期**：★ 期间上游新增了大量门文件与重测试 ⇒
#       ★ ★ 本轮 2026-11-09 本机**门内**实测最慢单包已是 **268.823s**（★ 隔离跑只有
#       ★ ★ 170~189s ⇒ ★★ **门内才是要用的那一档**）⇒ ★★★ ★ ★ **预算同步由 120s 提到
#       ★ ★ 600s**，★ 理由与数据见 `guards` target 上方那段注释）
#       ⇒ ★★★ **预算上限只放宽「不腰斩」，不放宽任何判据**。
#     · ★ **慢门（scripts/ursmcheck）不进 CI**：4 核 Linux 实测 **767s（13 分钟）**
#       ⇒ codeup Flow 的常规 job 预算装不下；且它依赖 `python3` 与 bash 工具链
#       （alpine 实测因缺 python3 而红——那是**环境缺件**，不是代码缺陷）。
#       ⇒ 慢门留在本地 `make guards` 与官方门 `verify.sh` 的 `go test ./...` 里。
#     · installer 跨模块门：实测 ~20~30s，同样零外部依赖 ⇒ 一并接进 CI。
#
#   ⇒ 新增这个 target 的意义：让「登记进 GUARD_PACKAGES」这件事**真的有 CI 在跑**。
.PHONY: guards-fast
guards-fast: guards-installer guards-shell guards-audit-ledger guards-gate-scripts guards-baseline-copies ## 运行**CI 预算内**的审计守卫（快门 + installer 跨模块门 + shell 门 + 跨文档登记册门 + 门脚本接线门 + 基线副本一致性门；不含 ursmcheck 慢门）
	$(GO) test $(GUARD_PACKAGES) -count=1 -timeout=600s

# scripts/guards/check-shell-fullwidth-var.sh —— 「裸 $VAR 紧跟非 ASCII 字节」门。
#
# ★ 2026-10-22 R107 轮（12h 审计 C5）接线：此前这个 shell 门**没有任何调用方**
#   （全仓检索排除 docs/ ⇒ 引用数 0：无 CI workflow、无 Makefile 目标、无脚本调用）
#   ⇒ CI 真正执行的是 Go 门（scripts/ursmcheck/cjk_var_expansion_test.go），
#     而这个 shell 版覆盖 703 个 .sh + .bash（含未跟踪文件）却**无人运行**
#     ⇒ 它的枚举面/判据会**静默腐坏**，而既往审计的修复表把它列为「门（已修）」。
#   ⇒ 两个门判据同源、扫描面已对齐（vendor 等跳过集逐项一致）⇒ 现在可以安全接线。
#   ⇒ 约 1~2s，无外部依赖。
#
# ★★ 2026-10-24 R147 轮（12h 审计）：★ **上面那句「判据同源」曾经变成假话**。
#   · 接线那天（10-22 12:55）两门确实同口径；
#   · 但 Go 门在 3c9e42d41（10-23 07:21）**升级了扫描器**
#     （加了「尾随注释」与「`\$` 转义」两条口径）⇒ **shell 门没跟着升**
#     ⇒ 两门判据漂移 ⇒ 976a2b8de（10-24 01:32，窗口内）声称
#       「收口【origin/main 上就红】的 CJK 门」时**只收了 Go 侧**
#       ⇒ shell 侧一直红 ⇒ **origin/main 上 `make guards` 就是红的**
#       （pristine 对拍实测 rc=1，报 5 处，**全是假阳性**：
#         2 处尾随注释 + 3 处转义美元 `\$remote_addr`）。
#   ⇒ 本轮把 shell 门的判据**对齐到 Go 门**（改门不改那 5 处代码 ——
#     那 5 处的写法是对的，改代码会破坏断言语义），
#     并新增 scripts/ursmcheck/shell_fullwidth_var_gate_test.go
#     **把「两门同源」钉成可执行断言**（同一个合成语料喂两门 ⇒ 比对命中集合；
#       变异台实测：把旧门换回去 ⇒ 3 条测试全红）。
#   ⇒ ★ 扫描面**故意**仍比 Go 门宽：Go 门只扫 git 追踪的文件（它要拿读数
#     对棘轮登记册 ⇒ 读数必须确定）；shell 门扫「已跟踪 ∪ 未跟踪未忽略」
#     ⇒ 新写好还没 git add 的脚本也在视野里 ⇒ 本门无登记册 ⇒ 宽面不引入不确定性。
.PHONY: guards-shell
guards-shell: ## 全角变量 shell 门（判据与 Go 门逐字一致；扫描面含未跟踪文件，比 Go 门宽）
	@bash scripts/guards/check-shell-fullwidth-var.sh

# scripts/ursmcheck 的门文件（**数量实时取**，见文件头 URSMCHECK_GATE_COUNT；
# 比主门慢一个量级 ⇒ 独立 timeout）
# ★★ 2026-10-31 r1050 轮补记：★ 本行原本写死「165 个门文件」⇒ ★★ 上游 2026-11-09
#   ★★ 把它手改成「166 个」⇒ ★★★ **而当时的真值是 180** ⇒ ★★★ **写死的数字必漂**
#   ★★★ （★ 手改的那个数**当场就是错的** ⇒ ★★ 比不改更坏：★ 它看起来是「核对过的」）
#   ⇒ ⇒ ★★ 改为**不写死**：★ 门文件数由 `scripts/checks/guards-sync.sh` 的
#   ★★ `find` **实时数**给出（★ `make guards-sync` 可见）⇒ ★★ **数字从此不可能过期**。
# ★★ 2026-10-28 r1044 轮：慢门预算由 900s 提到 **1800s**（★ 与 verify.sh 的
#   ★ 全量测试腿**同一档** ⇒ ★★ 两处必须同步改）。
#   依据：★ 仓内自记该包实测 **840.111s / timeout 900s**（⇒ 当时余量只剩 ~60s，
#   ★ 见 scripts/ursmcheck/shell_fullwidth_var_gate_test.go）；★ 该轮隔离实测
#   ★ **1011.711s**（★ load 26.8/30.2/36.4 ⇒ 上界）⇒ ★★ **越过了 900s** ⇒ ⇒
#   ★★ 超时被 SIGKILL ⇒ ★★ `testing` 的 panic 在 alarm goroutine 里 ⇒ 其它 goroutine
#   ★★ 的 `defer` 不执行 ⇒ ★★ **被跟踪的台账静默留空** ⇒ ⇒ 预算必须给足。
# ★★ 2026-10-30 r1050 轮：**回填当前实测数**（★ r1044 轮 §六-⑤ 登记的
#   「每轮结束前重新量一次慢门包的耗时并更新预算注释里的实测数」⇒ ★★ 预算与实测
#   的差就是余量 ⇒ ★★ **注释里的读数会自己过期** ⇒ 必须有人回填）。
#   本轮实测（`make guards-ursmcheck`，HEAD 5a4150440）：★ **1122.817s**
#   （load 6.32/5.12/3.47 ⇒ ★ 负载远低于 1011.711s 那次的 26.8/30.2/36.4）
#   ⇒ ★★★ **在更轻的负载下反而更慢** ⇒ ⇒ **不是负载抬高的上界，是包真的变重了**
#   （★ 上游 10 个提交里 `installer/internal/dbinit/runner.go` 改 194 行、
#   ★ 多个台账 TSV 大改 ⇒ 门要重算的比值更大）。
#   ⇒ ★★ 余量由「~1.8x」收窄到 **1800/1122.817 ≈ 1.60x** ⇒ ★★ **仍然够用**
#   （⇒ 预算不必再提；★ 但余量已非「宽松」，下一轮若继续变重需重新评估）。
.PHONY: guards-ursmcheck
guards-ursmcheck: ## scripts/ursmcheck 全量门（@URSMCK_GATE_COUNT@ 个门文件；★ 实时数，与 scripts/checks/guards-sync.sh 的 find 同源；CI 上此前从未执行过）
	$(GO) test $(GUARD_PACKAGES_SLOW) -count=1 -timeout=1800s

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
	# ★ 2026-11-01（12h 审计轮）：modelcredibility 的 rollup 真库契约腿
	#   （分数与结论必须取自同一行 —— 纯单测拼不出 SQL 聚合行为）。
	#   ★ 这条腿此前**没有任何入口** ⇒ 按本仓自己的纪律（「写了脚本」不等于
	#   「门在那里」——一个没人知道的命令等于没有命令」）等于门不存在。
	#   ★ 用**专用变量**（已登记 scripts/audit/dsn_exclusive_registry.tsv）：
	#   事实源统一下发的 TEST_DATABASE_URL 指向产品库，这条腿的 `_test` 后缀
	#   守卫必然 Fatal ⇒ 必须指向一次性 *_test 库。
	@test -n "$${TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL}" || (echo "TEST_MODELCREDIBILITY_ROLLUP_DATABASE_URL is required (dedicated *_test database)" && exit 1)
	TEST_PG_CONTRACTS_ISOLATED=1 $(GO) test -tags=integration ./domains/modelcredibility -run '^Test(RollupProviderModelRealPG|ConsensusStreakSurvivesPersistence)$$' -count=1 -v -timeout=90s

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
