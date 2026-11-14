#!/usr/bin/env bash
# 本机 go 验证口径：macOS 钉 cgo=0（完整理由见下方「本机 go 验证口径」一节；判据
# tests/verify_cgo_convention_test.sh 钉「export 必须在前 60 行内」⇒ 本块必须在
# r1048 的 gocache 头块**之前**，勿下移——2026-10-29 24h 审计轮曾因头块挤压而红）。
if [[ "$(uname -s)" == "Darwin" ]]; then
  export CGO_ENABLED="${VERIFY_CGO_ENABLED:-0}"
fi
echo "[verify] CGO_ENABLED=${CGO_ENABLED:-<unset: 宿主默认 cgo>}"
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# verify.sh — unified local/CI verification gate for llm-gateway-go.
#
# Usage:
#   ./verify.sh                 # backend gate + govulncheck
#   ./verify.sh --web            # backend gate + frontend typecheck/build
#                                # (CHROME_BIN 已设置时额外跑 web-mobile layout-audit/settle 正控)
#   ./verify.sh --skip-govulncheck  # diagnostic only; never use as release evidence

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$REPO_ROOT"

RUN_WEB=false
SKIP_GOVULNCHECK=false
for arg in "$@"; do
  case "$arg" in
    --web) RUN_WEB=true ;;
    --skip-govulncheck) SKIP_GOVULNCHECK=true ;;
    -h|--help)
      sed -n '2,9p' "$0"
      exit 0
      ;;
    *)
      echo "unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

# ── 本机 go 验证口径（2026-10-14 登记）─────────────────────────────────
#
# 本机 macOS SDK 27.0 与 CLT linker 不兼容：任何**走 cgo 的形态**——`go build`
# 的链接步、以及编进 SQLite 驱动的测试二进制——一律死在 clang 的
# `tapi error: malformed file` / `unknown architecture arm64e.x1-macos`。
# 2026-10-14 实测（同一 commit、同一台机）：
#     go build ./...              → RC=1（121 行 tapi/clang 报错，仅链接步失败）
#     CGO_ENABLED=0 go build ./... → RC=0
# 这是宿主工具链的结构性缺口，不是代码回归——因此**在 macOS 上**一切 go 验证钉
# cgo=0。作用域刻意按宿主划：Linux/CI 上 cgo 本来可用，cgo=0 会让 6 个 lite/sqlite
# 包的用例整片红（driver_nocgo.go 按设计 panic），所以那边一个字节都不改。
# 需要在 macOS 上强制 cgo 时用 VERIFY_CGO_ENABLED=1。
#
# 判据：tests/verify_cgo_convention_test.sh（已接入本文件下方）
# （export 本体已上移至文件头——r1048 头块把它挤出 head -60 契约区，见上。）
echo "[verify] cgo convention contracts"
bash tests/verify_cgo_convention_test.sh

echo "[verify] db252 tunnel and sync shell contracts"
bash tests/db252_tunnel_test.sh

# 2026-10-07：部署脚本曾因裸 GNU `timeout` 在 macOS 上恒 127，导致 docker 探测整块
# 被静默跳过（commit df69ef0be 修掉）。契约测试本身依赖共享 SSOT、跑不进 CI，
# 因此单挂这道零外部依赖的聚焦门防同类回归。
echo "[verify] deploy timeout portability contracts"
bash tests/deploy_timeout_portability_test.sh

# 2026-10-14：magpie P0 观测脚本曾因 154 二进制名 llm-gateway-go≠gateway、
# 裸 journal unit、pipefail+grep 零命中中途死 → 授权后观测假 FAIL。
# 零外部依赖门锁住双名解析 / canary 日志 / 反假绿量具 / 部署硬门。
echo "[verify] magpie P0 observe contracts"
bash tests/observe_magpie_p0_contract_test.sh

# 2026-10-10：资源探测改为「主机优先」（host → docker → 自动安装）。
# 旧顺序先扫 docker，扫到就把 mode 定成 docker，主机上真跑着的 pg/redis 被跳过；
# 而 docker-less 机器会直接 die 在 "Docker Compose is unavailable"。
# 这道门零外部依赖（mock 二进制即可跑），锁住：探测顺序、docker 不得覆写主机
# 已认领的槽位、维护库探活（应用库还没建时也要认领）、以及自动安装先于 docker 失败。
echo "[verify] deploy host-first resource contracts"
bash tests/deploy_host_first_contract_test.sh
echo "[verify] deploy session write environment contracts"
bash tests/deploy_session_write_env_test.sh
echo "[verify] rollback running-service verification"
bash tests/deploy_rollback_service_test.sh
# 2026-10-16 生产实测事故：把 64 位 sha256 明文写进发给 psql 的 SQL，
# 输出脱敏管道把它替换成 [REDACTED:*] 替身（67 字符、含非十六进制字符），
# 污染值被 UPDATE 真正提交进 llm_gateway_migration_checksums。台账对齐必须
# 走 convert_from(decode('<hex>','hex'),'UTF8')，明文 sha256 永不出现。
echo "[verify] migration-ledger checksum transport"
bash tests/ledger_checksum_hex_transport_test.sh
# 2026-10-10：252 上 pg17-disk-watch.sh 堆到 310 份副本、149 个僵死
# `docker exec ... psql`，把 15G 内存吃到 swap 100% / load 300，PG 因此
# 无法服务 154 网关（SQLSTATE 57P03、一小时 194 个 HTTP 503）。
# 根因是本目录巡检**普遍**缺单实例闸门与查询超时，属于会再次发生的形态，
# 因此单挂这道零外部依赖的聚焦门防同类回归（静态部分任何 runner 都能跑，
# 行为部分需要 flock，在 Linux 上才执行）。
echo "[verify] 252-monitor single-instance gate contracts"
bash tests/monitor_single_instance_gate_test.sh

# 2026-10-11：2533 部署轮遗留 4 个孤儿网关（stop_instance 只发 TERM 不等待、
# cutover 重试后旧 pid 失去 pidfile 引用）。这道零外部依赖门锁住
# TERM→KILL 升级与部署收尾 reap_stray_gateways 的行为（含变异验证）。
echo "[verify] deploy-local orphan gateway reap contracts"
bash tests/deploy_local_reap_test.sh

# 2026-10-13：192.168.31.211 的 Docker 形态部署。零外部依赖门锁住
# · Redis 库号（用户口中的 "tableid"）必须从 15 往下倒排，不能从 0 抢；
# · claim 持久化使重跑不漂移（否则每次部署换库 = 缓存全丢）；
# · PG 复用判定必须同时校验 PG17 与 columnar 两个维度；
# · $var 紧跟全角括号在 UTF-8 locale 会被并入变量名（unbound variable）。
echo "[verify] deploy-211 docker contracts"
bash tests/deploy_211_contract_test.sh

# 2026-10-28：/version 身份链（版本戳谎报家族）的**行为门**。
# 上一轮只加了实现（fail-closed die）没加测试 ⇒ 有功能没牙齿；且当时的实现
# 有两处真缺陷（显式给的号被 git 覆盖、--via 路径的门根本不执行）。这道门
# 把引擎里那段身份闸的真代码抠出来驱动，断言它**真的拒绝**（三个变异臂：
# 显式号被覆盖 / 第一道门降级为 log / --via 侧的门被删，逐个被抓红）。
# ★ 零外部依赖：纯 bash，不碰 docker、不碰目标机。
echo "[verify] deploy seamless version identity (behavior)"
bash tests/deploy_seamless_version_identity_test.sh

# 2026-10-23：211 的 nginx X-Real-IP 修补此前**从来没生效过**（引擎只在自己的
# conf 里找「按域 443 块」，而 211 的 443 腿在通用 TLS 终结器里 ⇒ 每次部署
# 打一条**假**告警「块形态变了?」）。判定逻辑已挪到
# scripts/ops/nginx-llm-realip-patch.py ⇒ 这道门用**夹具**驱动它，
# 钉住结论分类与副作用（改谁 / 不改谁 / 改前备份 / nginx -t 失败要还原）。
# ★ 零外部依赖：纯 bash + python3，不碰 nginx、不碰任何目标机。
echo "[verify] nginx llm realip patch behavior"
bash tests/nginx_llm_realip_patch_test.sh

# 2026-10-23（211 502 事故根修）：「upstream 端口」这条**事实**的读写与体检。
# 引擎此前用**不剥注释**的正则读/改 upstream 端口 ⇒ 会读到 nginx 注释里那个
# 旧端口（245 实况）⇒ 活跃槽判错 ⇒ 可能切错端口或停掉正在服务的槽 ⇒ 502。
# 这道门用夹具钉住：读（烤死/include/backup/注释陷阱）、写（只改活的上游行、
# 备份写改前内容、include 形态 fail-closed）、只读体检（脱节 ⇒ 退出码 2）。
echo "[verify] nginx upstream port facts"
bash tests/nginx_upstream_port_test.sh

# ★ 2026-10-24 12h 审计轮（问题⑥）：这两道门此前**从来没有被执行过** ——
#   `tests/deploy_legacy_restart_flag_test.sh`（69dc5ffee 修「--legacy-restart 死旗标」
#   的防回归门）与 `tests/deploy_211_teardown_order_test.sh`（211 拆解顺序门）
#   **除自身以外没有任何引用** ⇒ 不在 verify.sh、不在 CI ⇒ ⇒ 「门是绿的」这句话
#   当时**没有依据**（门根本没跑）。
#   ⇒ 接线。★ 两道门都是纯 bash、零外部依赖。
echo "[verify] deploy-seamless --legacy-restart flag regression"
bash tests/deploy_legacy_restart_flag_test.sh

echo "[verify] deploy 211 teardown order"
bash tests/deploy_211_teardown_order_test.sh

echo "[verify] pre-commit checks"
./scripts/pre-commit-check.sh

echo "[verify] migration channel contracts"
bash scripts/apply-db-revision-sequence_test.sh

echo "[verify] migration checksums (sql/migrations/startup vs docs/db-changelog.md)"
./scripts/verify-migration-checksums.sh --quiet

echo "[verify] full Go tests"
# 目标集保持 `./...`，**不做任何剔除/白名单**：AGENTS.md 的既有策略是「主门在本机
# 长期为红是维护者决定，不要加白名单」——加剔除等于替维护者决定哪些失败可以不看。
# 本机 cgo=0 下 SQLite 真实驱动不编入（driver_cgo.go ↔ driver_nocgo.go 双形态，
# nocgo 分支按设计 panic「lite 存储模式需 CGO 构建」），下列 6 个包在本宿主必然红，
# 它们的真实覆盖在 CI 的 cgo=1 上：
#   bg / cmd/gateway / storage / storage/factory / storage/sqlite / tests/integration
# 该名单的漂移报警在 tests/verify_cgo_convention_test.sh。
#
# ★★ 2026-10-24 12h 审计轮（问题②）：超时预算由 300s 提到 **900s**。
#   起因：`scripts/ursmcheck` 里的**掏空法门**会**真的掏空仓内被跟踪的 .tsv**
#   再跑读方 ⇒ 它是窗口内最慢的一个包（本机实测 293.047s；另一台机器实测 444s）。
#   而 `testing` 的超时是 **alarm goroutine 里的 panic** ⇒ 其它 goroutine 的
#   `defer` **不执行** ⇒ 一旦被超时 SIGKILL ⇒ **被跟踪的台账静默留空**。
#   ⇒ 300s 预算下实测余量只有 **7 秒** ⇒ 负载一高就中招。
#   ⇒ 本次同时给该门加了「备份落盘 + 自愈预检」（scripts/ursmcheck/tsv_hollowout_safety_test.go），
#     即使仍被杀，下一次跑会自动把留空的台账还原回去。
#   ⇒ 两道防线一起上：**给足预算**（不让它被杀）＋ **被杀也能自愈**。
#   ⚠ 提高的是**每包**预算，不是总时长：各包并行执行，总墙钟由最慢的包决定。
# ★★ 2026-10-28 r1044 轮：超时预算由 900s 提到 **1800s**。
#   起因：★ 2026-10-24 那次把预算提到 900s 时，**同一条**包已经实测占到 **840.111s**
#   （★ scripts/ursmcheck/shell_fullwidth_var_gate_test.go 里记着这个数，★ 且当时
#   ★ 就写明「余量只剩 ~60s」）⇒ ★★ 也就是说 **900s 的余量当时就已经没了**。
#   ★ 本轮实测：★ 隔离跑 `go test ./scripts/ursmcheck/ -count=1 -timeout=1800s`
#   ★ = **1011.711s**（★ 读数窗口 load average 26.8/30.2/36.4 ⇒ ★ 同一时刻机器上
#   ★ 还有第二个 ursmcheck 套件在跑 ⇒ ★★ 该数是**上界**）⇒ ★★ **越过了 900s**
#   ★★ ⇒ ⇒ `go test ./...`（★ 默认单包超时 10m）会 `panic: test timed out after 10m0s`
#   ★★ ⇒ ⇒ 而 `testing` 的超时是 **alarm goroutine 里的 panic** ⇒ 其它 goroutine 的
#   ★★ `defer` **不执行** ⇒ ★★ **被跟踪的台账静默留空**（★ 2026-10-24 那段注释写的
#   ★★ 正是这个后果）⇒ ⇒ ★★ 预算必须重新给足。
#   ⇒ 选 1800s 的依据：★ 覆盖本轮实测上界 1011.711s 并留 ~1.8x 余量；★ 与
#   ★ ★ `make guards-ursmcheck`（Makefile 的 GUARD_PACKAGES_SLOW 腿）**同一档**——
#   ★ ★ 两处必须同步改（★ 一处改一处不改 ⇒ 慢门与官方门会给出不同的结论）。
# ★★ 2026-10-30 r1050 轮：**回填当前实测数**（★ r1044 轮 §六-⑤ 登记的「每轮结束前
#   重新量一次慢门包的耗时并更新预算注释里的实测数」⇒ ★★ 注释里的读数会自己过期）。
#   本轮实测（`make guards-ursmcheck`，HEAD 5a4150440）：★ **1122.817s**
#   （load 6.32/5.12/3.47 ⇒ ★ 负载远低于 1011.711s 那次的 26.8/30.2/36.4）
#   ⇒ ★★★ **在更轻的负载下反而更慢** ⇒ ⇒ **不是负载抬高的上界，是包真的变重了**
#   ⇒ ★★ 余量由「~1.8x」收窄到 **1800/1122.817 ≈ 1.60x** ⇒ ★★ **仍然够用**
#   （★ 预算不必再提；但 ★ 余量已非「宽松」⇒ 下一轮若继续变重需重新评估）。
#   ⚠ 提高的仍只是**每包**预算，不是总时长：各包并行执行，总墙钟由最慢的包决定。
#   ⚠ 这**不是**「把阈值调大以让检测变松」：★ 超时是 harness 的**杀开关**，
#   ★ ★ 调大它不会让任何一道门从红变绿 ⇒ ★★ 它只会让一个**健康但慢**的包不被腰斩。
# ★★ 2026-11-13（全面审计v3 53 增量移交轮）：1800s → **3600s** —— 上一段 r1050
#   # 预约的「下一轮若继续变重需重新评估」由本轮兑现。★ 新证据（全在**冷构建图**上）：
#   # 掏空门为每个读方包现编译测试二进制（隔离 GOCACHE）⇒ 树一变单门就 **36min+**
#   # （Slice-2 轮三跑实证：30m 全量撞死 / 45m×2 单包通过）⇒ 1800s 冷图必腰斩；
#   # 暖图实测 766s→952s 漂移 +186s（53 增量）继续恶化。与 `make test` /
#   # `make guards-ursmcheck` **三处同档**（r1050 通用式：一处改三处不改 ⇒ 慢门与
#   # 官方门会给出不同结论）。★ 并发会话第三维度：同 checkout 的 solo 掏空跑
#   # 25m 预算实测 panic（1500.707s）＋ 并发全包 2400s 实测 user CPU 仅 234s
#   # ⇒ 95% 在等整仓掏空锁 ⇒ 预算上界含锁排队（≤15min/条目，hollowoutLockWait）。
#   # ★★ 2026-11-13 二次上调 → 7200s：满套件 3600s 在掏空门 51m15s 探中途中被杀
#   # （当日第二起悬崖）⇒ 掏空门单条目 600s×2 探针 × ~30 条目的最坏情形越过任何
#   # 中等固定预算；拆独立通道 = 结构解（翻转条件 = 7200s 常态机仍越线）。
#   # ★★ 2026-11-13 拆通道轮终态：7200s → **3600s**。掏空门家族（tsv_hollowout_gate /
#   #   structured_registry_hollowout / 负控）已拆到 `guards-hollowout` 专用通道
#   #   （build tag `ursmhollowout`，14400s 预算）⇒ build tag ⇒ 不再进默认构建 ⇒
#   #   本全量腿重回到静态门口径（≈900-1100s 实测）⇒ 3600s ≈ 3x 余量。
#   #   ★ 与 `make test` / `make guards-ursmcheck` 三处同档（r1050 通用式）。
make test-plugin-runtime-modules
go test ./... -count=1 -timeout=3600s

# ★ 2026-11-11（IR 协议转换层审计轮）：把**从未被运行过**的门接进官方门。
# ★ 这三个变异 harness + 覆盖率门禁原先**只有** `.github/workflows/` 里的调用方
#   ⇒ ★★ 而本仓远端是 codeup ⇒ `.github/workflows/` 从来不在执行路径上
#   ⇒ ⇒ **接线了却跑不到** ⇒ 按本仓纪律「门没有调用方 = 声明不是门」，
#   它们此前**不是门，是文件**。
# ★ 本文件的写法约定是**直接调脚本**（不调 `make`）⇒ 与上面那批快门同款。
# ★ 实测（macOS / darwin-arm64 / go1.27.2）：热 ≈26s、冷 ≈46s
#   ⇒ 远低于本文件里 270s / 1122s 那一档慢门 ⇒ 进官方门零预算压力。
# ★ `set -euo pipefail` ⇒ 任一 harness 变红 ⇒ 本文件立刻中止 ⇒ 门有牙。
echo "[verify] IR protocol conversion guards (mutation harness x4 + coverage gate)"
bash scripts/audit/ir-client-error-fix-mutation.sh
bash scripts/audit/ir-gemini-only-loss-mutation.sh
bash scripts/audit/ir-coerce-numeric-mutation.sh
# ★ 2026-11-11（协议转换审计轮）：第 4 个 harness = 通用变异验证器的
#   **toolcall-canonical 组**（守 D1–D5 的「工具调用规范视图」族，10 臂）。
#   ★ 用 `--group` 而不是整文件跑 ⇒ ★ 那份文件还含其它轮别的包的臂 ⇒
#   ★ 整文件跑会把别的关注点拖进 IR 段的预算 ⇒ ★★ 而「组」是按关注点分的 ★★。
python3 scripts/audit/mutation-verify-2026-11-11.py --group toolcall-canonical
go test ./internal/ir/... -count=1 -timeout=600s -coverprofile=coverage.ir.out -covermode=atomic
COV=$(go tool cover -func=coverage.ir.out | awk '/^total:/ {gsub("%","",$3); print $3}')
echo "[verify] internal/ir coverage: ${COV}% (gate >= 75%)"
awk -v c="$COV" 'BEGIN { if (c+0 < 75) { printf "coverage %.2f%% < 75%% gate\n", c; exit 1 } }'

# 2026-10-14（r1031 §五-3 / 判据纪律 #11）：TestStorageAudit* 集成审计是三域存储
# 契约（冷读一致性/跨实例一致性/V2 元数据配对）的唯一观测器，带 -tags=integration
# 且需要一次性回环 Redis+PG——上面的 `go test ./...` 从不编译它们，此前无任何入口
# 真实执行；首次执行即抓到真缺陷（F3）。缺少依赖或观测到不一致均拒绝发布。
# 2026-11-02（第二十四轮）：★ 把**从未被运行过**的门接进强制路径。
# ★ 这批门此前**没有任何调用方** ⇒ 按本仓纪律「门没有调用方 = 声明不是门」，
#   它们只是文件；★ 其中两条已经**坏掉或红着**，而没人知道：
#   · tests/session_storage_audit_gate_test.sh 依赖 ripgrep（CI 精简镜像没有）
#     ⇒ 本文件此前在**任何**没有 ripgrep 的环境上都是红的；
#   · tests/deploy_seamless_state_machine_test.sh 间接需要 bash ≥ 4
#     ⇒ macOS 自带 bash 3.2 下 cryptic 崩 ⇒ 本轮补了可执行守卫
#     （读 BASH_VERSINFO + 明确指引 + exit 64 ⇒ 从 cryptic 崩变成可执行地拒绝）
#     ⇒ ★ 它**已接线**（本文件 + `make guards`），但**不进 CI 快门**：
#       实测 **270s** 而 CI 快门预算是 **116s**（见 .workflow/main-verify.yml）
#       ⇒ 与仓里既有的慢门（guards-ursmcheck 1122s）处理方式一致。
# ★ 下面这 **13** 条全部**零外部依赖**（实测单条 **0–14s**、macOS bash 3.2 全绿）
#   ⇒ 可安全进 CI 快门。
echo "[verify] orphan gate scripts brought back into the mandatory path (13 fast gates)"
bash tests/audit_154_wrapper_test.sh
bash tests/credential_script_contract_test.sh
bash tests/deploy_seamless_mobile_surface_test.sh
bash tests/selfcheck_diagnostics_contract_test.sh
bash tests/bump_version_collision_test.sh
bash tests/deploy_blue_green_contract_test.sh
bash tests/deploy_lock_test.sh
bash tests/deploy_port_rotation_test.sh
bash tests/deploy_promotion_test.sh
bash tests/deploy_readiness_contract_test.sh
bash tests/deploy_sops_test.sh
bash tests/deploy_local_contract_test.sh
# ★ 2026-11-04（F-15）：共享部署库入口的解析/失败契约门（新增，≈0.3s）
bash tests/shared_lib_resolution_contract_test.sh
# ★ 2026-11-04（F-18 / F-22）：秘密扫描器「--tracked-only 枚举覆盖」行为门（新增，≈1s）
#   ★ 它钉住的是「★ 非 ASCII / 含空格的文件名 ★」**不被静默丢弃** ★
#   ⇒ ★ 而这条枚举分支正是两道公开仓闸门（pre-push ＋ sync-to-github）用的那一条 ★
bash tests/scan_secrets_enumeration_contract_test.sh

# ★ 白名单正则可移植性门（★ 钉「★ 白名单 alternation 在各平台都能编译 ★」★）。
#   这道门是因为 scan-secrets.sh:94 那条未转义花括号的白名单模式
#   （★ glibc 判整条 alternation 编译失败 ⇒ ★ CI 侧白名单整体失效 ★）而立。
#   ⇒ 详见 docs/12小时内修订审计-20261104-0329.md §8.20
bash tests/scan_secrets_whitelist_regex_portability_test.sh

# ★ 扫描器正则口径确定性门（★ 钉「★ 词边界可移植 ＋ 发现/归因同口径 ★」★）。
#   ★ 起因：★ 同一棵树、同一扫描面（18211 文件），macOS 读 180 条、glibc 读 1325 条
#   ⇒ ★ 差 1145 条、★ 100% 落在 INTERNAL_IP 一族（★ `\b` 在 BSD libc 的 regcomp 里
#   ★ 不是词边界 ⇒ ★ 那三条规则在 macOS 上整族失效 ★）★
#   ★ 另一条是【★ BLOCK 级假阴性 ★】：★ 发现阶段大小写不敏感、★ 归因阶段敏感
#   ⇒ ★ 大写形态的凭据被静默丢弃 ★。
#   ⇒ 详见 docs/12小时内修订审计-20261104-0329.md §8.21
bash tests/scan_secrets_regex_determinism_test.sh

# ★ 切流状态机门（慢门，实测 270s ⇒ 不进 CI 快门，只在官方门与 make guards 上跑）。
#   经 scripts/run-bash4-gate.sh：找得到 bash ≥ 4 ⇒ 真跑；找不到 ⇒ 显式 SKIP
#   （rc=0，**不伪装成通过**）。
echo "[verify] deploy seamless state machine gate (slow gate, via bash4 wrapper)"
bash scripts/run-bash4-gate.sh tests/deploy_seamless_state_machine_test.sh

echo "[verify] session storage audit gate decision contracts"
bash tests/session_storage_audit_gate_test.sh
echo "[verify] session storage audit integration suite (required coherence)"
bash tests/session_storage_audit_gate.sh

echo "[verify] privacy compliance tests"
./scripts/verify-privacy-compliance.sh

echo "[verify] Go vet"
go vet ./...

echo "[verify] build-tag compile matrix (every tag configuration)"
# 上面那条 go vet 不带 tag ⇒ 只在某个 tag 下才参与编译的文件**无人编译**。
# 2026-10-02 实测：admin 包在 -tags=integration 下编译不过（undefined:
# v1DirectTables），而默认配置与主干 CI 全绿。详见脚本文件头。
./scripts/check-build-tags.sh

echo "[verify] gateway build"
go build ./cmd/gateway

if [[ "$SKIP_GOVULNCHECK" == true ]]; then
  echo "[verify] WARNING: govulncheck skipped; result is diagnostic only" >&2
else
  ./scripts/govulncheck.sh
fi

if [[ "$RUN_WEB" == true ]]; then
  # pnpm 是官方路径（CI/发布），npm 并行受支持；选哪个由 scripts/lib/node-pm.sh
  # 单点决定，不在这里各写一份。WEB_PM=pnpm|npm 可强制。
  # shellcheck source=scripts/lib/node-pm.sh
  source "$REPO_ROOT/scripts/lib/node-pm.sh"
  echo "[verify] frontend lockfile sync gate"
  ./scripts/check-frontend-lockfiles.sh
  echo "[verify] frontend install"
  pm_install web
  echo "[verify] frontend typecheck"
  pm_run typecheck web
  echo "[verify] frontend tests"
  pm_run test web
  echo "[verify] frontend static gates (responsive breakpoints + el-* imports)"
  pm_run responsive:check web
  pm_run element:check web
  echo "[verify] frontend color token gate (rule 12 P0 + baseline)"
  pm_run color:check web
  echo "[verify] frontend copy-quality gate (AI 腔判据 + per-bucket ratchet)"
  # ★ 2026-10-25 接入：这道门此前只扫语言包，本轮扩到 src/** 源码文本面与
  #   移动端（web-mobile/）。不接进 CI ⇒ 扩出来的覆盖面就只是装饰。
  pm_run copy-quality:check web
  echo "[verify] frontend i18n gates (hidden-key / shared-dead / copy-clarity / empty-state)"
  # ★★ 2026-10-26 接入（12h 审计轮）：这四道门此前**只登记在 package.json**，
  #   ★★ verify.sh / workflow / vitest **一处都没引用** ⇒
  #   ⇒ 「只降不升的受门管棘轮」（死键族）与「清晰文案门」**从未被执行过**
  #   ⇒ ⇒ ★ **一门没人跑的守卫是文档，不是门**（R86/R94 同族）。
  #   ⇒ ⇒ 接入前实测：四道门在当前树上**全绿** ⇒ ⇒ 接入是零风险的。
  #   ★ 逐道说明（为什么它们该被执行）：
  #   · i18n:hidden-key:check      隐藏键（组件里注册了但没真用）——省体积但易删错
  #   · i18n:shared-dead:check     跨语种共用的死键计数棘轮（只许降不许升）
  #   · copy-clarity:check          文案清晰度（源码侧，不只是语言包侧）
  #   · copy-clarity-locale:check   文案清晰度（语言包侧，含占位符/通用短语）
  #   · i18n:empty-state:check     空状态 5 分结构门（第 14 增量新增）
  pm_run i18n:hidden-key:check web
  pm_run i18n:shared-dead:check web
  pm_run copy-clarity:check web
  pm_run copy-clarity-locale:check web
  pm_run i18n:empty-state:check web
  # ★★ 2026-10-30 r1050 轮接入：「前端门总清单」单一入口（web-gates-runner.mjs）
  #   ★★ 本轮实测：`web/package.json` 里有 32 个 `:check` 类门，而 verify.sh 只登记了 9 个
  #   ★★ ⇒ ★★★ **19 个门在 CI 上一次都没被执行过**
  #   ★★★ （★ 其中就有 `i18n:locale-value:check` —— ★★ 语言包值棘轮门）
  #   ★★★ 同样地，**web 侧 31 个 `:selftest` 一个都没进强制路径**（★ 只有 web-mobile 的
  #   ★★★ `gate:selftest` 在）⇒ ★★ **一道门自己的判据坏了（恒绿/恒红）没人知道**。
  #   ⇒ ⇒ 接入「清单 + 一个入口」而非把 50 条命令塞进本文件（★ 清单是单一真值）。
  #   ⇒ ★★ 入口**自己**校验清单里的每个脚本都存在于 package.json ⇒ ★ 清单写错 ⇒ 入口自己红。
  #   ⇒ ★★ 刻意排除项（★ 不静默 ⇒ 每次运行都列出 + 理由）：`ui:sweep:selftest`
  #   ★★ （需 playwright + chromium ⇒ 环境缺件 ⇒ CI 不装浏览器）。
  pm_run web-gates:run web
  echo "[verify] frontend build"
  pm_run build web
  echo "[verify] web-mobile install"
  pm_install web-mobile
  echo "[verify] web-mobile unit gates (contrast/token ratchets + view regressions)"
  pm_run test web-mobile
  echo "[verify] web-mobile static-gate selftests (node-only)"
  pm_run gate:selftest web-mobile
  # R50-A2 遗留 #3 收口：layout-audit / settle 两条正控的判据跑在真实浏览器里，
  # node-only 门代替不了。CI runner 装 Chrome 后注入 CHROME_BIN 即自动升级为必过门；
  # 未注入时显式跳过并说明——不静默、也不假装跑过。
  if [[ -n "${CHROME_BIN:-}" ]]; then
    echo "[verify] web-mobile layout-audit positive control (Chrome: $CHROME_BIN)"
    pm_run audit:selftest web-mobile
    echo "[verify] web-mobile settle positive control (Chrome: $CHROME_BIN)"
    pm_run audit:settle web-mobile
  else
    echo "[verify] SKIP web-mobile layout-audit/settle positive controls (CHROME_BIN 未设置; runner 安装 Chrome 并导出 CHROME_BIN 后自动接入)"
  fi
fi

if rg -n '^(<<<<<<<|>>>>>>>)' --glob '!vendor/**' --glob '!web/node_modules/**' .; then
  echo "[verify] merge conflict marker found" >&2
  exit 1
fi

echo "[verify] PASS"
