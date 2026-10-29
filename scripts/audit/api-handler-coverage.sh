#!/usr/bin/env bash
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
#
# api-handler-coverage.sh —— HTTP 路由 handler 的**运行时零覆盖**审计器
#
# ==============================================================================
# ① 口径（★ 本工具到底在测什么 —— 先读这一段，否则读数会被误用）
# ==============================================================================
#
# ★★ 「运行时零覆盖」= **coverprofile 里该 handler 函数体范围内没有一个语句块的
#    执行次数 > 0**。
#
# ★★ 它**不是**「测试文件里没出现过这个名字」。
#    （那是**静态**口径：名字没出现在任何 _test.go 里 ⇒ 只能说明「没有**直接**调用
#      这个名字的测试」，不能说明「运行时没走到」—— 间接路径、httptest 夹具、
#      表驱动、反射包装都能走到。两种口径**不可互推**。）
#
# ★★ 读数的来源 = `go test ./admin/ -coverprofile=…` 的**真实执行计数**。
#    ⇒ 它是「**这个包里现有单测跑起来时**，哪些 handler 的函数体真的被执行过」。
#    ⇒ 它**不**包含 `cmd/gateway` 等其它包的测试 ⇒ 那部分路由只能落到
#      `undetermined`（见 ③ 局限）。
#
# ==============================================================================
# ② 三类的语义
# ==============================================================================
#
#   covered        落在该 handler 函数体范围内**存在**执行次数 > 0 的语句块
#                  ⇒ 现有单测**确实执行过**这段逻辑。
#
#   zero_coverage  找到了 handler 的函数定义，且其函数体范围内**有**语句块，
#                  但**全部**执行次数 = 0
#                  ⇒ ★ 这就是「逐个 api 单元测试」的真实缺口清单。
#
#   undetermined   **第三类，不许混进 zero_coverage**：
#                  - 抽不出 handler 名（闭包字面量 / 构造函数 / 跨包方法值），
#                  - 同名定义有多个且**无法区分**（⇒ 宁可不判，也不猜错），或
#                  - 找到了定义，但**函数体范围内一个语句块都没有**
#                    （最常见：该 handler 定义在 `cmd/gateway` 等**不在本次
#                      coverprofile 范围内**的包里 ⇒ 本工具**看不见**它）。
#
# ==============================================================================
# ③ ★★ 已知局限（★ 读数时必须一起读，否则会把下界当点估计）
# ==============================================================================
#
# ★★ `undetermined` 那一类**可能是被覆盖的** —— 它只表示「本工具判不了」，
#    **不**表示「没被覆盖」。
#    ⇒⇒ **零覆盖比例是【下界】**：真实零覆盖数 ≥ 本工具报出的 zero_coverage 数。
#
# ★★ 覆盖面局限：coverprofile 只来自 `./admin/` 一个包 ⇒ `cmd/gateway`、
#    `taskprofile`、`internal/quality` 等包的路由**必然**大量落到 undetermined。
#    ⇒⇒ 想把这一类也判掉，需要把那些包也跑一遍 `-coverprofile`（本工具的
#    `--cover` 参数就是为「合并多包 profile」预留的）。
#
# ★★ 闭包字面量注册的路由（`mux.HandleFunc("/x", func(w,r){…})`）**判不了**
#    —— 它没有名字，而它的函数体**可能**已被覆盖。
#
# ★★ 静态与动态的差别：coverprofile 只统计**被执行的语句块**；
#    一个 handler 若只走了一部分分支，覆盖率会是 > 0 但 < 100% ⇒ 本工具
#    判它是 `covered`（判定阈值 = 0）。**「有覆盖」≠「测全了」**。
#
# ==============================================================================
# ④ 怎么复跑
# ==============================================================================
#
#   # 全量（会跑 ./admin/ 的全部单测，实测 ≈ 95 s）
#   scripts/audit/api-handler-coverage.sh --out /tmp/ahc.tsv
#
#   # 快速模式（不跑 go test，只验证机制；需要一份现成的 coverprofile）
#   scripts/audit/api-handler-coverage.sh \
#       --cover /path/to/cover.out \
#       --routes /path/to/routes.tsv \
#       --out /tmp/ahc-small.tsv
#
#   # 把零覆盖清单写成例外台账（**保留**已有台账里的人工裁决）
#   scripts/audit/api-handler-coverage.sh \
#       --ledger scripts/ursmcheck/api_handler_coverage_ledger.tsv
#
# ★ 依赖：**零第三方运行时依赖**（只用 POSIX shell + awk + 核心工具）。
#   `go` 只在「需要现跑 coverprofile」时被调用（那是 Go 工具链本身，不是第三方库）。
#   校验和用 `sha256sum` / `shasum -a 256` / `openssl dgst -sha256`（三选一）。
#
# ★ 输出：TSV（`path \t method \t handler \t verdict \t pct \t def_file`）
#   + 末尾汇总（三类条数 + 覆盖率）。
# ==============================================================================

set -euo pipefail

AHK_ROOT_DEFAULT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AHK_ROOT="$AHK_ROOT_DEFAULT"
AHK_COVER=""
# ★ R157 轮新增：★ 要跑 coverprofile 的包（★ 默认 admin）。
#   ★ 理由：★ 「undetermined」里有 167 条是「**消歧失败**」——★ 它们的 handler 定义
#   ★ **在别的包里**（★ 例：`ListApprovals` 在 `api/approval_handler.go`）
#   ★ ⇒ ★★ 只跑 admin 的 profile ⇒ ★★ 看不见它们的函数体 ⇒ ★★ 判不了。
# ★★★ R157 第二轮修正（★ 实测打出来的）：★ 默认包集**不能拍脑袋**，
#   ★ ★ 要么是「handler 定义的包」（★ 由读数反推，★ 见 --two-pass），
#   ★ ★ 要么是「注册路由的包」（★ admin / cmd/gateway）。
#   ★ ⇒ ★ 默认改成 **admin,cmd/gateway,api,taskprofile,internal/quality**
#   ★   （★ 实测：★ 606 条路由的 def_file **只**落在这 5 个包里）。
AHK_PKGS="admin,cmd/gateway,api,taskprofile,internal/quality"
# ★ --two-pass：★ 第一遍粗读 → 推导「定义包」→ 第二遍用 -coverpkg 联合覆盖重跑。
#   ★ 默认开（★ ★ 因为「各包各跑」会漏掉跨包执行 ⇒ ★ 实测 8642 个块受影响）。
AHK_TWO_PASS="1"
# ★ --no-probe：★ 内部标志（★ **递归调用自己**时用）⇒ ★ 本层**不再**去推「定义包」。
# ★ ★ 缺了它 ⇒ ★ `--cover` 分支会**无限递归**（★ R157 轮实测：进程树 4 层同款 bash）。
AHK_NO_PROBE="0"
AHK_ROUTES=""
AHK_OUT=""
AHK_LEDGER=""
AHK_SCAN_DIRS=""     # 空 = 全仓（排除 vendor/tests/web/node_modules/_test.go）
# ★ 工具自己的仓内相对路径（★ 第二遍要**递归调用自己**来推「定义包」）。
AHK_TOOL_REL="scripts/audit/api-handler-coverage.sh"

AHK_USAGE() {
  cat <<'EOF'
用法：
  api-handler-coverage.sh [选项]

选项：
  --root DIR      仓库根（默认：脚本所在位置的上上级）
  --cover FILE    直接使用已有的 coverprofile（跳过 go test）
  --pkgs LIST     要跑 coverprofile 的 Go 包（逗号分隔，
                  ★ 默认 "admin,cmd/gateway,api,taskprofile,internal/quality"
                  ★ —— ★ 实测：★ 606 条路由的 def_file **只**落在这 5 个包里）
  --two-pass N    两遍跑（1=开，0=关，★ 默认 1）：
                  ★ 第一遍粗读 → ★ 推导「handler 定义包」→ ★ 第二遍用
                  ★ **-coverpkg 联合覆盖**重跑 ⇒ ★★ 跨包执行才看得见
                  ★ ★（★ 实测：★ 「各包各跑」会漏判，★ 8642 个语句块受影响）。
  --routes FILE   路由清单（TSV：path \t method \t handler \t reg_file
                  [\t recvVar \t recvType]）—— 只审计清单里的路由（快速模式用）
  --out FILE      TSV 输出到 FILE（不给则输出到 stdout）
  --ledger FILE   把「零覆盖」清单写成例外台账行（保留已有台账的人工裁决）
  --scan-dirs D   只扫描这些目录（逗号分隔，相对仓库根）—— 快速模式用；
                  不给则扫全仓（排除 vendor/tests/web/node_modules/_test.go）
  -h, --help      显示本帮助

退出码：0 = 成功；2 = 参数/路径错误；3 = 环境不可用（缺 go / go test 失败）。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --root)   AHK_ROOT="${2:?--root 需要参数}"; shift 2 ;;
    --cover)  AHK_COVER="${2:?--cover 需要参数}"; shift 2 ;;
  --pkgs)   AHK_PKGS="${2:?--pkgs 需要参数}"; shift 2 ;;
    --two-pass) AHK_TWO_PASS="${2:-1}"; shift 2 ;;
    --no-probe) AHK_NO_PROBE="1"; shift ;;
    --routes) AHK_ROUTES="${2:?--routes 需要参数}"; shift 2 ;;
    --out)    AHK_OUT="${2:?--out 需要参数}"; shift 2 ;;
    --ledger) AHK_LEDGER="${2:?--ledger 需要参数}"; shift 2 ;;
    --scan-dirs) AHK_SCAN_DIRS="${2:?--scan-dirs 需要参数}"; shift 2 ;;
    -h|--help) AHK_USAGE; exit 0 ;;
    *) echo "★ 未知参数：$1" >&2; AHK_USAGE >&2; exit 2 ;;
  esac
done

AHK_ROOT="$(cd "$AHK_ROOT" && pwd)"
# ★ ★★ R157 轮修正：★ 把 `$0`（脚本自身路径）**解析成绝对路径**。
#   ★ 缘由：★ 台账头要写「工具自己的内容 sha」⇒ ★ 需要在**任何 cwd 下**都能打开这个文件
#   ★ ★★ 而脚本中段会 `cd "$AHK_ROOT"` ⇒ ★★ 相对路径的 `$0` 在那之后会失效
#   ★ ★★ ⇒ ★★ 必须在**任何 cd 之前**解析。
AHK_SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# ★★ 包名口径固定为 ./admin/（与本工具的读数口径绑定）—— 路径不存在 ⇒ **报错退出**，
#    绝不静默跳过（静默跳过 = 分母变成 0 = 「零覆盖」变成恒真命题）。
if [ ! -d "$AHK_ROOT/admin" ]; then
  echo "★ 仓库根下不存在 admin/ 目录：$AHK_ROOT/admin" >&2
  echo "  ⇒ 包名口径（./admin/）不成立 ⇒ 拒绝出读数（退出码 2）" >&2
  exit 2
fi

AHK_TMP="$(mktemp -d "${TMPDIR:-/tmp}/api-handler-coverage.XXXXXX")"
trap 'rm -rf "$AHK_TMP"' EXIT

# ------------------------------------------------------------------------------
# sha256（fingerprint 用）—— 三选一，都没有 ⇒ 明确失败（不许静默降级）
# ------------------------------------------------------------------------------
if command -v sha256sum >/dev/null 2>&1; then
  AHK_SHA_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
  AHK_SHA_CMD="shasum -a 256"
elif command -v openssl >/dev/null 2>&1; then
  AHK_SHA_CMD="openssl dgst -sha256"
else
  echo "★ 找不到 sha256 工具（sha256sum / shasum -a 256 / openssl dgst -sha256）" >&2
  echo "  ⇒ fingerprint 无法计算 ⇒ 拒绝出读数（退出码 3）" >&2
  exit 3
fi

ahk_sha256() {
  if [ "$AHK_SHA_CMD" = "sha256sum" ]; then
    printf '%s' "$1" | sha256sum | cut -d' ' -f1
  elif [ "$AHK_SHA_CMD" = "shasum -a 256" ]; then
    printf '%s' "$1" | shasum -a 256 | cut -d' ' -f1
  else
    printf '%s' "$1" | openssl dgst -sha256 | awk '{print $NF}'
  fi
}

# ★★★ R157 轮修正：**文件内容**的 sha256（★ 与 ahk_sha256 不同！）。
#
# ★★ 为什么必须另写一个（★ 本轮实测踩到）：
#   ★ `ahk_sha256` 是**对字符串**求哈希（★ 它的用途是 fingerprint = sha256(handler@def_file)）
#   ★ ⇒ ★★ 我最初拿它算「工具自己的 sha」⇒ ★★ 算出来的是**【路径字符串】的哈希**
#   ★★ ⇒ ★★ 与「文件内容的哈希」**永不相等** ⇒ ★★ 门（★ 算的是内容哈希）**永远红**
#   ★★ ⇒ ★★★ **症状**：★ 门报「台账是用旧工具生成的」⇒ ★★ 而工具**刚刚**生成它
#   ★★★ ⇒ ★★★ **「门红」的第一反应不该是「台账过期」，★ 也可能是「两边算的不是同一个东西」**。
ahk_file_sha256() {
  local f="$1"
  if [ "$AHK_SHA_CMD" = "sha256sum" ]; then
    sha256sum "$f" | cut -d' ' -f1
  elif [ "$AHK_SHA_CMD" = "shasum -a 256" ]; then
    shasum -a 256 "$f" | cut -d' ' -f1
  else
    openssl dgst -sha256 "$f" | awk '{print $NF}'
  fi
}

# ==============================================================================
# 第 1 步：拿到 coverprofile
# ==============================================================================
if [ -z "$AHK_COVER" ]; then
  if ! command -v go >/dev/null 2>&1; then
    echo "★ PATH 里没有 go ⇒ 无法产出 coverprofile" >&2
    echo "  ⇒ 装 Go，或用 --cover FILE 直接喂一份现成的 coverprofile（退出码 3）" >&2
    exit 3
  fi
  # ★★★ R157 轮改：★ 跑**多个**包并合并 profile。
  #
  # ★★★ 三条硬约束（★ 每条都是被实测打出来的，★ 不是设计洁癖）：
  #
  # ★ ① **必须用 -coverpkg 联合覆盖**（★ 不是各包各跑各的）。
  #    ★ 动机（★ 实测）：★ 一个 handler 的定义体在包 A，★ 而**调用它的测试**在包 B
  #      ⇒ ★★ 若只跑 `go test ./A/`，★ A 的 profile 里那个块 count=0
  #      ⇒ ★★ 被误判成「零覆盖」（★ **假阳性** ⇒ ★ 台账会凭空多出一条假缺口）。
  #    ★★ 实测证据：★ 5 个包联合覆盖后，★ **8642 个语句块**出现
  #      「一份 profile 里 count=0、另一份里 count>0」⇒ ★★ 跨包执行**确实存在**
  #      ⇒ ★★ 「各包自跑」口径**必然漏判**（★ 实测多判出 3 条真零覆盖：
  #      `internal/quality` 的 CalculateNow / ListProfiles / GetProfile）。
  #    ★★ -coverpkg 的语义：★ 「本包测试运行时，★ **被覆盖插桩**的这些包里
  #      哪些代码的**执行计数**要被记录」⇒ ★★ 跨包调用**才看得见**。
  #
  # ★ ② **合并必须按语句块去重、count 取 max**。
  #    ★ 动机（★ 实测）：★ Go 的多包 profile 合并（cmd/go/internal/test/cover.go
  #      的 mergeCoverProfile）就是 **`io.Copy` 追加**，★ **不去重**。
  #      ⇒ ★ 5 份 profile 朴素追加 = 99003 行 ⇒ ★ 唯一块只有 36441 个
  #      ⇒ ★ **放大 2.72×** ⇒ ★★ 语句总数被放大、★ `pct` 失真。
  #    ★★ 去重键 = (file, startLine, endLine)；★ count 取 **max**
  #      （★ 「任一次执行即算覆盖」⇒ ★ 与 covered/zero_coverage 的判定语义一致）。
  #
  # ★ ③ **包清单必须来自「哪个包真的定义了 handler」**，★ 不是拍脑袋写一串。
  #    ★ 实测：★ 全部 606 条路由的 def_file 只落在 **5 个包**
  #      （admin / api / cmd/gateway / internal/quality / taskprofile）
  #      ⇒ ★★ 跑 `domains/...` 只会**引入失败**（★ `domains/nodestatecache` 的
  #      `TestSelectP99Under1ms` 是**计时敏感 flaky**）⇒ ★★ 整组被丢弃
  #      ⇒ ★★ 反而**拉低**了覆盖面。
  #    ★★ 但「定义包」在跑测试**之前**不知道（★ 要先扫源码）⇒ ★⇒
  #      ★ 工具**两遍跑**：★ 第一遍用**默认包集**产出 profile，★ 第二遍
  #      ★ **按第一遍读出的 def_file 分布**重算包集再跑一次（★ ★ 第二次的
  #      ★ coverpkg 才是准的）⇒ ★★ 读数自带「包集是推导出来的」这一口径。
  #
  # ★★★ 某个包失败**不许静默跳过**：★ 打印警告 + 在输出里报「N 个包失败」
  #     ⇒ ★★ 读数必须带口径（★ 覆盖面不全时，下界就更不准）。
  AHK_PKG_FAILED=""
  AHK_MERGED="$AHK_TMP/cover.out"
  AHK_PKG_IDX=0

  # ---- 跑一轮（★ $1 = 包列表，★ $2 = 本轮 coverpkg 列表）----
  ahk_run_pkgs() {
    local pkgs="$1" coverpkgs="$2"
    local out="$AHK_TMP/raw-cover.out"
    : >"$out"
    AHK_PKG_FAILED=""
    AHK_PKG_IDX=0
    local one cp_args=""
    for pkg in $pkgs; do
      AHK_PKG_IDX=$((AHK_PKG_IDX + 1))
      one="$AHK_TMP/cover-$AHK_PKG_IDX.out"
      if [ -n "$coverpkgs" ]; then
        cp_args="-coverpkg=$coverpkgs"
      else
        cp_args=""
      fi
      echo "… 跑 go test ./$pkg $cp_args -coverprofile（★ 第 $AHK_PKG_IDX 个包）" >&2
      # ★ 包规格：★ 直接用 "./$pkg"（★ **不带尾斜杠**）⇒ ★ `./admin` / `./domains/session/...`
      #   都合法（★ 带尾斜杠会让 `...` 通配变成 `.../` ⇒ go test 报错）。
      if ( cd "$AHK_ROOT" && go test "./$pkg" $cp_args -coverprofile="$one" -count=1 ) \
            >"$AHK_TMP/go-test-$AHK_PKG_IDX.log" 2>&1; then
        grep -v '^mode:' "$one" >>"$out" 2>/dev/null || true
      else
        echo "★ go test ./$pkg 失败（末尾 20 行）⇒ ★ 该包**不进** profile ⇒ ★ 覆盖面有缺" >&2
        tail -20 "$AHK_TMP/go-test-$AHK_PKG_IDX.log" >&2 || true
        AHK_PKG_FAILED="${AHK_PKG_FAILED} $pkg"
      fi
    done
    if [ -n "$AHK_PKG_FAILED" ]; then
      echo "★ 失败的包：$AHK_PKG_FAILED ⇒ ★ 本轮读数的覆盖面**小于**预期 ⇒ ★ 零覆盖下界更不精确" >&2
      # ★★★ R157 轮补：★ 失败**不许**只打一行警告然后照常 exit 0。
      #   ★ 缘由（★ 实测）：★ `cmd/gateway` 偶发失败（★ 3 次复跑全过 ⇒ 属 flaky）
      #     ⇒ ★★ 那一轮的读数从 150/349/107 退化成 129/347/130
      #     ⇒ ★★ 而工具**退出码仍是 0** ⇒ ★★ 调用方（CI / 脚本 / 人）**无从察觉**
      #     ⇒ ★★ 一份**覆盖面缺一包**的读数会被**当成权威读数**用（★ 台账也会照它生成）。
      #   ⇒ ★★ 现在：**读数照出**（供诊断），★ 但**退出码非 0**（★ 3 = 环境/覆盖不全）。
      #   ⇒ ★★ 「不许静默降级」的落点就是**退出码**，★ 不是 stderr 上的一句话。
      AHK_PKG_FAILED_GLOBAL="${AHK_PKG_FAILED_GLOBAL:-}${AHK_PKG_FAILED}"
    fi
    # ---- 去重合并：★ 按 (file,start,end) 取 max count ----
    #   ★ ★ 用 awk 而不是「sort -u」：★ sort -u 只能去重**整行**，
    #     而**同一个块在不同 profile 里 count 不同**（★ 实测 8642 个）
    #     ⇒ ★★ 必须按 key 归并后取 max ⇒ ★★ 这正是「任一次执行即算覆盖」。
    awk -v OFS='\t' '
      {
        # 行格式：<file>:<s>.<c>,<e>.<c> <numStmt> <count>
        loc = $1; n = $2; c = $3
        key = loc
        if (key in seen) {
          if (c + 0 > cnt[key] + 0) cnt[key] = c + 0
          if (n + 0 > nstmt[key] + 0) nstmt[key] = n + 0
        } else {
          seen[key] = 1; cnt[key] = c + 0; nstmt[key] = n + 0; order[++k] = key
        }
      }
      END {
        printf "mode: set\n"
        for (i = 1; i <= k; i++) printf "%s %d %d\n", order[i], nstmt[order[i]], cnt[order[i]]
      }
    ' "$out" >"$AHK_MERGED"
  }

  # ---- 第一遍：★ 默认包集 + 无 coverpkg（★ 先要一份粗读数来推包集）----
  ahk_run_pkgs "${AHK_PKGS//,/ }" ""

  # ---- 第二遍：★ 按第一遍读出的 def_file 分布推导「定义包」⇒ 重跑 ----
  #   ★ 为什么要第二遍：★ -coverpkg 的清单必须是「**所有定义包**」才能看见跨包执行；
  #   ★ 而定义包要靠读数才知道 ⇒ ★⇒ 先粗后精。
  if [ "$AHK_TWO_PASS" = "1" ]; then
    # ★ ★ 递归调用自己时必须关掉两遍跑（★ 否则无限递归）。
    # ★ ★ 子调用把 TSV **打到 stdout**（★ 不给 --out ⇒ 不依赖任何跨进程的临时文件
    #   ⇒ ★★ 不会踩「子进程退出时 trap 删目录」这类坑）。
    #   ★ 注意：子调用没给 --out 时，summary 会打到 **stderr**（★ 工具的既有行为）
    #   ⇒ ★ 这里 2>/dev/null 丢掉 summary，★ 只取 stdout 的 TSV。
    AHK_DEFPKGS="$(bash "$AHK_ROOT/$AHK_TOOL_REL" --root "$AHK_ROOT" \
        --cover "$AHK_MERGED" --two-pass 0 --no-probe 2>/dev/null \
        | awk -F'\t' 'NR>1 && $6!=""{ n=split($6,a,"/"); p=a[1]; for(i=2;i<n;i++) p=p"/"a[i]; print p }' \
        | LC_ALL=C sort -u | tr '\n' ' ')"
    if [ -n "$AHK_DEFPKGS" ]; then
      echo "… 第二遍：★ 推导出的「定义包」= ${AHK_DEFPKGS}⇒ ★ 按它重跑（★ coverpkg 联合覆盖）" >&2
      AHK_COVERPKG=""
      for p in $AHK_DEFPKGS; do
        AHK_COVERPKG="${AHK_COVERPKG}./$p/,"
      done
      AHK_COVERPKG="${AHK_COVERPKG%,}"
      # ★ 跑测试的包集 = 定义包 ∪ 原始包集（★ 定义包自己也要跑，★ 否则它们自己的
      #   测试打出的覆盖会丢）⇒ ★ 取并集。
      AHK_ALLPKGS="$(printf '%s\n%s\n' $AHK_DEFPKGS ${AHK_PKGS//,/ } | LC_ALL=C sort -u | tr '\n' ' ')"
      ahk_run_pkgs "$AHK_ALLPKGS" "$AHK_COVERPKG"
    fi
  fi
  AHK_COVER="$AHK_MERGED"
else
  if [ ! -f "$AHK_COVER" ]; then
    echo "★ --cover 指定的文件不存在：$AHK_COVER" >&2
    exit 2
  fi
  # ★ 标记：★ 这份 profile 是**外部提供**的 ⇒ ★ 台账头必须如实写「external」
  #   ⇒ ★★ 不许写成「联合覆盖」⇒ ★★ 「只记事实」原则。
  AHK_COVER_EXTERNAL="1"
  # ★ ★ 死循环防线（★ R157 轮实测踩到）：★ 下面这个「推定义包」的递归调用
  #   **自己**也会走 `--cover` 分支 ⇒ ★⇒ 无条件递归 ⇒ ★⇒ 无限 fork
  #   （★ 实测：进程树里 4 层同款 bash + 一堆 mavis-trash 在删临时目录）。
  #   ⇒ ★⇒ 递归调用必须带 `--no-probe`（★ 本层自己**不再**推定义包）。
  if [ "$AHK_NO_PROBE" != "1" ]; then
    # ★ 定义包仍可从这份 profile 的读数里推出来 ⇒ ★ 记下来（★ 有助复核）。
    AHK_DEFPKGS="$(bash "$AHK_ROOT/$AHK_TOOL_REL" --root "$AHK_ROOT" \
        --cover "$AHK_COVER" --two-pass 0 --no-probe 2>/dev/null \
        | awk -F'\t' 'NR>1 && $6!=""{ n=split($6,a,"/"); p=a[1]; for(i=2;i<n;i++) p=p"/"a[i]; print p }' \
        | LC_ALL=C sort -u | tr '\n' ' ')"
  fi
fi
if [ ! -s "$AHK_COVER" ]; then
  echo "★ coverprofile 是空的：$AHK_COVER ⇒ 分母为 0 ⇒ 拒绝出读数" >&2
  exit 3
fi

# ==============================================================================
# 第 2 步：源码扫描 —— 路由登记 + 函数定义索引
#
# 扫描范围：全仓（排除 vendor/、tests/、_test.go、web/、node_modules/）。
# ★ 为什么不用「只扫 admin/ + cmd/gateway/」：登记点散落在多个包
#   （cmd/gateway、cmd/gateway-v2、cmd/quality-service、internal/quality、
#    taskprofile、api、cmd/test_quality_api …），漏扫 = 分母偏小 = 读数偏乐观。
# ==============================================================================
AHK_SRC_LIST="$AHK_TMP/srclist.nul"
(
  cd "$AHK_ROOT"
  if [ -n "$AHK_SCAN_DIRS" ]; then
    # ★ 快速模式：只扫指定目录（仍排除 vendor/tests/web/node_modules/_test.go）
    OLDIFS="$IFS"; IFS=','
    for d in $AHK_SCAN_DIRS; do
      IFS="$OLDIFS"
      [ -n "$d" ] || continue
      if [ ! -d "$AHK_ROOT/$d" ]; then
        echo "★ --scan-dirs 指定的目录不存在：$AHK_ROOT/$d" >&2
        exit 2
      fi
      find "$AHK_ROOT/$d" \
        -name '*.go' \
        -not -path "$AHK_ROOT/vendor/*" \
        -not -path "$AHK_ROOT/tests/*" \
        -not -path "$AHK_ROOT/web/*" \
        -not -path "$AHK_ROOT/node_modules/*" \
        -not -path "$AHK_ROOT/.git/*" \
        -not -name '*_test.go' \
        -print0
      IFS=','
    done
    IFS="$OLDIFS"
  else
    find "$AHK_ROOT" \
      -name '*.go' \
      -not -path "$AHK_ROOT/vendor/*" \
      -not -path "$AHK_ROOT/tests/*" \
      -not -path "$AHK_ROOT/web/*" \
      -not -path "$AHK_ROOT/node_modules/*" \
      -not -path "$AHK_ROOT/.git/*" \
      -not -name '*_test.go' \
      -print0
  fi
) >"$AHK_SRC_LIST"

AHK_DEFS="$AHK_TMP/defs.tsv"
AHK_ROUTES_RAW="$AHK_TMP/routes_raw.tsv"

cat >"$AHK_TMP/scan.awk" <<'AWK_SCAN'
# ---- 去注释 + 路由登记 + 函数定义索引 ----------------------------------------
# ★ 兼容 POSIX awk（BWK awk / mawk / gawk 均可）：不用 match(s,re,arr) 三参形式，
#   不用 ENDFILE / gensub 等 GNU 扩展，也不用 arr[k][j] 嵌套下标。
#
# 输出：
#   DEFS   = dir \t name \t file \t startLine \t endLine \t isMethod \t recvVar \t recvType
#   ROUTES = path \t method \t name \t regFile \t line \t cands \t recvs \t envRecvVar \t envRecvType

function codeline(raw,   i, c, out, n, sl) {
  # 返回「去掉注释后的代码」；同时维护全局 inblock（跨行的 /* */ 注释状态）。
  # ★ 快路径：行里既没有 '/' 也没有 '"' ⇒ 不可能有注释起始、也没有字符串 ⇒ 原样返回。
  if (!inblock && index(raw, "/") == 0 && index(raw, "\"") == 0) return raw
  out = ""; n = length(raw); sl = 0
  for (i = 1; i <= n; i++) {
    c = substr(raw, i, 1)
    if (inblock) {
      if (c == "*" && substr(raw, i + 1, 1) == "/") { inblock = 0; i++ }
      continue
    }
    if (sl) {
      if (c == "\\") { i++; out = out c; continue }
      if (c == "\"") { sl = 0 }
      out = out c
      continue
    }
    if (c == "\"") { sl = 1; out = out c; continue }
    if (c == "/" && substr(raw, i + 1, 1) == "/") { break }
    if (c == "/" && substr(raw, i + 1, 1) == "*") { inblock = 1; i++; continue }
    out = out c
  }
  return out
}

function recvpart(code,   s, i) {
  # 返回 "var|type"（接收者），无接收者返回 ""
  s = code
  sub(/^func[ \t]+/, "", s)
  if (substr(s, 1, 1) != "(") return ""
  i = index(s, ")")
  if (i == 0) return ""
  s = substr(s, 2, i - 2)
  sub(/^[ \t]+/, "", s)
  v = ""
  if (match(s, /^[A-Za-z_][A-Za-z0-9_]*/)) v = substr(s, 1, RLENGTH)
  sub(/^[ \t]*[A-Za-z_][A-Za-z0-9_]*[ \t]*/, "", s)
  sub(/^[ \t]*\*/, "", s)
  sub(/^[ \t]+/, "", s)
  t = ""
  if (match(s, /^[A-Za-z_][A-Za-z0-9_]*/)) t = substr(s, 1, RLENGTH)
  return v "|" t
}

function funcname(code,   s, i) {
  s = code
  sub(/^func[ \t]+/, "", s)
  if (substr(s, 1, 1) == "(") {
    i = index(s, ")")
    if (i == 0) return ""
    s = substr(s, i + 1)
  }
  sub(/^[ \t]+/, "", s)
  if (match(s, /^[A-Za-z_][A-Za-z0-9_]*/)) return substr(s, 1, RLENGTH)
  return ""
}

function hasrecv(code,   s) {
  s = code
  sub(/^func[ \t]+/, "", s)
  return (substr(s, 1, 1) == "(") ? 1 : 0
}

function bal(e,   i, c, n, d) {
  n = length(e); d = 0
  for (i = 1; i <= n; i++) {
    c = substr(e, i, 1)
    if (c == "(") d++
    else if (c == ")") d--
  }
  return d
}

function lastchain(c,   cand, lastc) {
  lastc = ""
  while (match(c, /\.[A-Za-z_][A-Za-z0-9_]*/)) {
    cand = substr(c, RSTART, RLENGTH)
    sub(/^\./, "", cand)
    lastc = cand
    c = substr(c, RSTART + RLENGTH)
  }
  return lastc
}

function chainlist(e,   s, out) {
  s = e; out = ""
  while (match(s, /[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+/)) {
    out = (out == "" ? substr(s, RSTART, RLENGTH) : out "|" substr(s, RSTART, RLENGTH))
    s = substr(s, RSTART + RLENGTH)
  }
  return out
}

function chainrecv(chain,   p) {
  # "a.b.c" ⇒ 去掉最后一段后取首段标识符 ⇒ "a"
  p = chain
  sub(/\.[A-Za-z_][A-Za-z0-9_]*$/, "", p)
  if (p == chain) return ""
  if (match(p, /^[A-Za-z_][A-Za-z0-9_]*/)) return substr(p, 1, RLENGTH)
  return ""
}

function chainhint(chain,   p, last) {
  # "a.b.c" ⇒ 接收者是 "a.b" ⇒ 提示词取 "b"（字段名 ⇒ 用来匹配 handler 的**类型**）
  # "a.b"   ⇒ 接收者是 "a"   ⇒ 没有字段名 ⇒ 返回 ""（改用所在函数的接收者类型）
  p = chain
  sub(/\.[A-Za-z_][A-Za-z0-9_]*$/, "", p)
  if (p == chain) return ""
  if (!match(p, /.*\.([A-Za-z_][A-Za-z0-9_]*)$/)) return ""
  last = substr(p, RSTART + 1, RLENGTH - 1)
  return last
}

function callee(e) {
  if (match(e, /^[A-Za-z_][A-Za-z0-9_]*\(/)) return substr(e, 1, RLENGTH - 1)
  return ""
}

function flushfile(   i, code, curfunc, curstart, currecv, curvar, curtype, mtext, pstart, pend,
                         pathlit, expr, j, expr0, method, path, chains, nch, k, parts,
                         cands, recvs, hints, name, nm, cl, mt, dirx, relx, rv, envvar, envtype, fk) {
  # ★ AHK_DIR / AHK_REL 是**全局**（不能做本函数的局部参数：
  #   局部参数会遮蔽 FNR==1 规则里赋的值 ⇒ dir/rel 全空 ⇒ 定义索引查不中）。
  dirx = AHK_DIR; relx = AHK_REL
  for (i = 1; i <= nlines; i++) C[i] = codeline(L[i])

  # ---- 函数定义索引 + 每个顶层函数的起止/接收者 ----
  nfunc = 0
  curfunc = ""; curstart = 0; currecv = 0; curvar = ""; curtype = ""
  for (i = 1; i <= nlines; i++) {
    code = C[i]
    if (curfunc != "") {
      if (code ~ /^}[ \t]*$/) {
        nfunc++
        FSTART[nfunc] = curstart; FEND[nfunc] = i; FVAR[nfunc] = curvar; FTYPE[nfunc] = curtype
        print dirx "\t" curfunc "\t" relx "\t" curstart "\t" i "\t" currecv "\t" curvar "\t" curtype > DEFS
        curfunc = ""
      }
      continue
    }
    if (code ~ /^func[ \t]/) {
      name = funcname(code)
      if (name != "") {
        curfunc = name; curstart = i; currecv = hasrecv(code)
        rv = recvpart(code)
        k = index(rv, "|")
        curvar = substr(rv, 1, k - 1); curtype = substr(rv, k + 1)
      }
    }
  }

  # ---- 路由登记 ----
  for (i = 1; i <= nlines; i++) {
    code = C[i]
    if (!match(code, /\.(HandleFunc|Handle)\("/)) continue
    # 找出「包含本行的顶层函数」⇒ 它的接收者类型就是 h 的类型
    envvar = ""; envtype = ""
    for (fk = 1; fk <= nfunc; fk++) {
      if (i >= FSTART[fk] && i <= FEND[fk]) { envvar = FVAR[fk]; envtype = FTYPE[fk]; break }
    }
    mtext = substr(code, RSTART, RLENGTH)
    pstart = RSTART + RLENGTH
    pend = index(substr(code, pstart), "\"")
    if (pend == 0) continue
    pathlit = substr(code, pstart, pend - 1)
    expr = substr(code, pstart + pend)
    sub(/^[ \t]*,[ \t]*/, "", expr)
    expr0 = expr
    # 表达式跨行 ⇒ 继续往后吃，直到括号配平
    j = i
    while (bal(expr) > 0 && j < nlines) {
      j++
      expr = expr " " C[j]
    }
    # ★ 闭包字面量 ⇒ 抽不出名字
    if (expr ~ /(^|[^A-Za-z0-9_])func[ \t]*\(/) {
      name = ""; cands = ""; recvs = ""; hints = ""
    } else {
      chains = chainlist(expr)
      nch = 0
      # ★ parts 是**数组**（split 的目标）⇒ 绝不能给它赋标量值
      #   （BWK awk 会报 "can't assign to ...; it's an array name"）。
      if (chains != "") nch = split(chains, parts, "|")
      cands = ""; recvs = ""; hints = ""
      if (nch > 0) {
        # 优先级 1：最后一条点号链的**最后一层**方法名
        #   h.handleX / admin(h.handleX) / h.superAdmin(h.handleX) / h.cfHandlers.listX
        #   / http.HandlerFunc(x.ServeHTTP) —— 全部命中「最后一层」
        name = lastchain(parts[nch])
        cands = name; recvs = chainrecv(parts[nch]); hints = chainhint(parts[nch])
        # 优先级 2：其余点号链（倒序）
        for (k = nch - 1; k >= 1; k--) {
          nm = lastchain(parts[k])
          if (nm != "" && nm != name) {
            cands = cands "|" nm
            recvs = recvs "|" chainrecv(parts[k])
            hints = hints "|" chainhint(parts[k])
          }
        }
      }
      # 优先级 3：顶层被调函数名（构造函数形态：NewCapabilitiesHandler(...)）
      cl = callee(expr0)
      if (cl != "" && cl != name) { cands = cands "|" cl; recvs = recvs "|"; hints = hints "|" }
      if (nch == 0 && cl == "") {
        if (match(expr0, /^[A-Za-z_][A-Za-z0-9_]*/)) name = substr(expr0, 1, RLENGTH)
      }
      if (name == "" && cl != "") name = cl
      if (name == "") {
        # 裸标识符（形如 pageUnavailable）
        if (match(expr0, /^[A-Za-z_][A-Za-z0-9_]*/)) name = substr(expr0, 1, RLENGTH)
      }
      # ★ 裸标识符没有点号链 ⇒ 把它本身也放进候选表，否则定义永远查不到
      if (cands == "" && name != "") { cands = name; recvs = ""; hints = "" }
    }
    # 方法前缀形态："GET /api/x"
    method = "ANY"
    path = pathlit
    if (match(pathlit, /^[A-Z]+ /)) {
      mt = substr(pathlit, 1, RLENGTH - 1)
      if (mt == "GET" || mt == "POST" || mt == "PUT" || mt == "DELETE" || mt == "PATCH" ||
          mt == "HEAD" || mt == "OPTIONS" || mt == "CONNECT" || mt == "TRACE") {
        method = mt
        path = substr(pathlit, RLENGTH + 1)
      }
    }
    print path "\t" method "\t" name "\t" relx "\t" i "\t" cands "\t" recvs "\t" hints "\t" envvar "\t" envtype > ROUTES
  }
}

BEGIN {
  inblock = 0; split("", L); nlines = 0; curfile = ""; DEFS = DEFSOUT; ROUTES = ROUTESOUT
  ROOT = ROOTDIR
}
FNR == 1 {
  if (curfile != "") flushfile()
  curfile = FILENAME
  AHK_REL = FILENAME
  if (substr(FILENAME, 1, length(ROOT) + 1) == ROOT "/") AHK_REL = substr(FILENAME, length(ROOT) + 2)
  AHK_DIR = AHK_REL
  sub(/\/[^\/]*$/, "", AHK_DIR)
  if (AHK_DIR == AHK_REL) AHK_DIR = "."
  split("", L); nlines = 0; inblock = 0
}
{ L[++nlines] = $0 }
END { if (curfile != "") flushfile() }
AWK_SCAN

: >"$AHK_DEFS"
: >"$AHK_ROUTES_RAW"
xargs -0 awk -v DEFSOUT="$AHK_DEFS" -v ROUTESOUT="$AHK_ROUTES_RAW" \
      -v ROOTDIR="$AHK_ROOT" -f "$AHK_TMP/scan.awk" <"$AHK_SRC_LIST" >/dev/null

# ==============================================================================
# 第 3 步：解析 coverprofile → 每个文件每个语句块的「语句数 / 执行次数」
# ==============================================================================
AHK_BLOCKS="$AHK_TMP/blocks.tsv"
cat >"$AHK_TMP/cover.awk" <<'AWK_COVER'
# coverprofile 行格式：<file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
# ★ 文件路径归一化成「仓库相对路径」：coverprofile 里带模块前缀
#   （github.com/<mod>/admin/x.go）或绝对路径 ⇒ 两种都要能对上源码。
BEGIN { ROOT = ROOTDIR; MOD = MODPATH }
{
  if ($0 ~ /^mode:/) next
  p = $1
  c = index(p, ":")
  if (c == 0) next
  f = substr(p, 1, c - 1)
  rest = substr(p, c + 1)
  if (ROOT != "" && substr(f, 1, length(ROOT) + 1) == ROOT "/") f = substr(f, length(ROOT) + 2)
  else if (MOD != "" && substr(f, 1, length(MOD) + 1) == MOD "/") f = substr(f, length(MOD) + 2)
  if (!match(rest, /^[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/)) next
  split(rest, pp, ",")
  sl = pp[1]; sub(/\..*$/, "", sl)
  el = pp[2]; sub(/\..*$/, "", el)
  print f "\t" sl "\t" el "\t" $2 "\t" $3
}
AWK_COVER
awk -v ROOTDIR="$AHK_ROOT" -v MODPATH="github.com/kaixuan/llm-gateway-go" \
  -f "$AHK_TMP/cover.awk" "$AHK_COVER" >"$AHK_BLOCKS"

# ==============================================================================
# 第 4 步：路由清单 —— 要么用 --routes 指定的清单，要么用源码扫出来的
# ==============================================================================
AHK_ROUTES_FINAL="$AHK_TMP/routes.tsv"
if [ -n "$AHK_ROUTES" ]; then
  if [ ! -f "$AHK_ROUTES" ]; then
    echo "★ --routes 指定的文件不存在：$AHK_ROUTES" >&2
    exit 2
  fi
  # 清单格式：path \t method \t handler \t reg_file [\t recvVar \t recvType]
  awk -F'\t' '
    /^#/ || NF == 0 { next }
    {
      if (NF < 4) {
        printf("★ --routes 清单行字段数 = %d（应 ≥ 4：path/method/handler/reg_file）：%s\n", NF, $0) > "/dev/stderr"
        exit 2
      }
      rv = ($5 == "" ? "" : $5); rt = ($6 == "" ? "" : $6)
      print $1 "\t" $2 "\t" $3 "\t" $4 "\t" 0 "\t" $3 "\t" rv "\t" "" "\t" rv "\t" rt
    }
  ' "$AHK_ROUTES" >"$AHK_ROUTES_FINAL"
else
  cp "$AHK_ROUTES_RAW" "$AHK_ROUTES_FINAL"
fi

# ==============================================================================
# 第 5 步：判定 —— covered / zero_coverage / undetermined
# ==============================================================================
AHK_RESULT="$AHK_TMP/result.tsv"
cat >"$AHK_TMP/judge.awk" <<'AWK_JUDGE'
BEGIN {
  FS = "\t"
  # ---- 函数定义索引（按 (包, 名字) 分组；同名可能有多个 ⇒ 靠接收者消歧）----
  while ((getline line < DEFSFILE) > 0) {
    n = split(line, a, "\t")
    if (n < 8) continue
    key = a[1] "\t" a[2]
    m = ++DEFCNT[key]
    DEFFILE[key, m] = a[3]; DEFSTART[key, m] = a[4] + 0; DEFEND[key, m] = a[5] + 0
    DEFMETH[key, m] = a[6] + 0; DEFRVAR[key, m] = a[7]; DEFRTYPE[key, m] = a[8]
    # ★★ R157 轮新增：**按名字**的全仓索引（★ 跨包回退用）。
    #   ★ 动机：★ 路由注册在 `admin` 包，★ 而 handler 的**定义体**在别的包
    #     （★ 例：`ListApprovals` 定义在 `api/approval_handler.go`）⇒ ★★ 旧口径
    #     只在**注册文件所在目录**找定义 ⇒ ★★ 找不到 ⇒ 判 undetermined
    #     ⇒ ★★ **而它既可能被覆盖、也可能没覆盖** ⇒ ★★ 下界被白白放大。
    nkey = a[2]
    nm = ++ALLCNT[nkey]
    ALLFILE[nkey, nm] = a[3]; ALLSTART[nkey, nm] = a[4] + 0; ALLEND[nkey, nm] = a[5] + 0
  }
  close(DEFSFILE)
  # ---- 语句块索引 ----
  while ((getline line < BLOCKFILE) > 0) {
    n = split(line, b, "\t")
    if (n < 5) continue
    f = b[1]
    k = ++NB[f]
    BS[f, k] = b[2] + 0
    BN[f, k] = b[4] + 0
    BC[f, k] = b[5] + 0
  }
  close(BLOCKFILE)
}
{
  path = $1; method = $2; hname = $3; regfile = $4; regline = $5 + 0; cands = $6
  recvs = $7; hints = $8; envvar = $9; envtype = $10
  dir = regfile
  sub(/\/[^\/]*$/, "", dir)
  if (dir == regfile) dir = "."

  # ---- 解析 handler 名（按候选优先级取第一个能**唯一**定位到函数的）----
  resolved = ""; deffile = ""; defstart = 0; defend = 0
  nch = split(cands, cl, "|")
  nrv = split(recvs, rv, "|")
  nht = split(hints, ht, "|")
  for (i = 1; i <= nch; i++) {
    if (cl[i] == "") continue
    rvi = (i <= nrv ? rv[i] : "")
    hti = (i <= nht ? ht[i] : "")
    key = dir "\t" cl[i]
    # ★ 这个候选**根本没有定义** ⇒ 可以试下一个候选
    #   （例如 NewCapabilitiesHandler(...)：主候选 "cfg.Listen" 无定义 ⇒ 退到顶层调用名）
    if (!(key in DEFCNT)) {
      # ★★ R157 轮新增：**跨包回退**。
      #   ★ 口径：★ 注册目录里没有这个定义 ⇒ ★ 但**全仓按名字唯一** ⇒ ★ 判它。
      #   ★★★ 两条硬约束（★ 缺一条就会误判）：
      #     ① **全仓唯一**（★ ALLCNT == 1）⇒ ★ 多义 ⇒ ★ 继续判不了（★ 不猜）。
      #     ② **该定义文件必须在 coverprofile 里**（★ deffile in NB）⇒ ★
      #        ★ 否则它的语句块**没有执行计数** ⇒ ★ 判出来的 covered/zero_coverage
      #        ★ **没有意义**（★ 会把「看不见」伪装成「零覆盖」⇒ ★ 违背本工具的核心纪律）。
      if ((cl[i] in ALLCNT) && ALLCNT[cl[i]] == 1) {
        cf = ALLFILE[cl[i], 1]
        if (cf in NB) {
          resolved = cl[i]; deffile = cf
          defstart = ALLSTART[cl[i], 1]; defend = ALLEND[cl[i], 1]
          break
        }
      }
      continue
    }
    ndef = DEFCNT[key]
    pick = 0
    if (ndef == 1) {
      pick = 1
    } else {
      # 消歧 A：接收者**类型**匹配（仅当路由里的接收者变量 == 所在函数的接收者变量时可信）
      if (rvi != "" && rvi == envvar && envtype != "") {
        m = 0
        for (j = 1; j <= ndef; j++) if (DEFRTYPE[key, j] == envtype) { m++; pick = j }
        if (m != 1) pick = 0
      }
      # 消歧 B：接收者**字段名**匹配（h.freePoolSSE.HandleStream ⇒ "freePoolSSE" ⇒ 类型含它）
      if (pick == 0 && hti != "") {
        m = 0
        for (j = 1; j <= ndef; j++) {
          if (index(tolower(DEFRTYPE[key, j]), tolower(hti)) > 0) { m++; pick = j }
        }
        if (m != 1) pick = 0
      }
      # 消歧 C：接收者**变量名**匹配
      if (pick == 0) {
        m = 0
        for (j = 1; j <= ndef; j++) if (DEFRVAR[key, j] == rvi) { m++; pick = j }
        if (m != 1) pick = 0
      }
    }
    # ★★ 歧义 ⇒ **立刻停**：不许再退到别的候选（那些多半是「包装器」方法，
    #   拿包装器的覆盖率去代表 handler = 静默错判）。
    #   ⇒ 宁可判 undetermined（= 下界），也不猜。
    if (pick == 0) break
    resolved = cl[i]
    deffile = DEFFILE[key, pick]; defstart = DEFSTART[key, pick]; defend = DEFEND[key, pick]
    break
  }
  display = hname
  if (resolved != "") display = resolved
  else if (display == "" && nch > 0) display = cl[1]

  verdict = "undetermined"; pct = "0.0"; nstmt = 0
  if (resolved != "") {
    tot = 0; hit = 0; nb = 0
    if (deffile in NB) {
      for (k = 1; k <= NB[deffile]; k++) {
        if (BS[deffile, k] >= defstart && BS[deffile, k] <= defend) {
          nb++
          tot += BN[deffile, k]
          if (BC[deffile, k] > 0) { hit += BN[deffile, k] }
        }
      }
    }
    nstmt = tot
    if (nb > 0) {
      if (hit > 0) {
        verdict = "covered"
        pct = sprintf("%.1f", 100.0 * hit / tot)
      } else {
        verdict = "zero_coverage"
        pct = "0.0"
      }
    }
    # nb == 0 ⇒ 范围内没有任何语句块 ⇒ 判不了 ⇒ undetermined（★ 不许混进零覆盖）
  }
  print path "\t" method "\t" display "\t" verdict "\t" pct "\t" deffile "\t" resolved "\t" nstmt "\t" regfile "\t" regline
}
AWK_JUDGE

awk -v DEFSFILE="$AHK_DEFS" -v BLOCKFILE="$AHK_BLOCKS" \
  -f "$AHK_TMP/judge.awk" "$AHK_ROUTES_FINAL" \
  | LC_ALL=C sort -t$'\t' -k1,1 -k2,2 -k3,3 >"$AHK_RESULT"

# ==============================================================================
# 第 6 步：输出 TSV（path / method / handler / verdict / pct / def_file）
# ==============================================================================
AHK_SUMMARY="$AHK_TMP/summary.txt"
awk -F'\t' '
  {
    v = $4
    total++
    if (v == "covered") cov++
    else if (v == "zero_coverage") zero++
    else und++
  }
  END {
    printf "路由登记总数: %d\n", total + 0
    printf "  covered       : %6d  (%.1f%%)\n", cov + 0, (total ? 100.0 * cov / total : 0)
    printf "  zero_coverage : %6d  (%.1f%%)\n", zero + 0, (total ? 100.0 * zero / total : 0)
    printf "  undetermined  : %6d  (%.1f%%)\n", und + 0, (total ? 100.0 * und / total : 0)
    printf "判定口径覆盖率 = covered / 路由登记总数\n"
    printf "★ zero_coverage 是【下界】：undetermined 那一类【可能已被覆盖】\n"
  }
' "$AHK_RESULT" >"$AHK_SUMMARY"

emit_tsv() {
  printf 'path\tmethod\thandler\tverdict\tpct\tdef_file\n'
  awk -F'\t' '{ print $1 "\t" $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6 }' "$AHK_RESULT"
}

if [ -n "$AHK_OUT" ]; then
  emit_tsv >"$AHK_OUT"
  cat "$AHK_SUMMARY"
else
  emit_tsv
  cat "$AHK_SUMMARY" >&2
fi

# ==============================================================================
# 第 7 步：--ledger ⇒ 把「零覆盖」清单写成例外台账行
#
# ★ carry-over 语义：**同 fingerprint 的旧行的人工裁决不许被覆盖**
#   （adjudication / reason / owner 三列逐字保留）。
# ★ 新出现的零覆盖项 ⇒ 写初稿态：adjudication=pending、reason 留空
#   ⇒ 门会拒绝它 ⇒ 逼人逐条裁决（★ reason 列是人工裁决，工具不许自动生成）。
# ==============================================================================
if [ -n "$AHK_LEDGER" ]; then
  AHK_OLD="$AHK_TMP/ledger_old.tsv"
  if [ -f "$AHK_LEDGER" ]; then
    cp "$AHK_LEDGER" "$AHK_OLD"
  else
    : >"$AHK_OLD"
  fi

  # 旧台账：fingerprint → (adjudication, reason, owner)
  awk -F'\t' '
    /^#/ { next }
    $1 == "path" { next }
    NF < 14 { next }
    { print $14 "\t" $10 "\t" $11 "\t" $13 }
  ' "$AHK_OLD" >"$AHK_TMP/old_by_fp.tsv"

  # fingerprint 映射：sha256(handler@def_file) 前 12 位
  awk -F'\t' '$4 == "zero_coverage" && $6 != "" { print $3 "@" $6 }' "$AHK_RESULT" \
    | LC_ALL=C sort -u >"$AHK_TMP/keys.txt"
  : >"$AHK_TMP/keymap.tsv"
  while IFS= read -r k; do
    [ -n "$k" ] || continue
    printf '%s\t%s\n' "$k" "$(ahk_sha256 "$k")" >>"$AHK_TMP/keymap.tsv"
  done <"$AHK_TMP/keys.txt"

  # 新台账行：只取 zero_coverage；按 (handler, def_file, path) 排序并编 occ
  awk -F'\t' -v OLDFP="$AHK_TMP/old_by_fp.tsv" -v KEYMAP="$AHK_TMP/keymap.tsv" '
    BEGIN {
      while ((getline line < OLDFP) > 0) {
        n = split(line, a, "\t")
        if (n < 4) continue
        OLD[a[1]] = a[2] "\t" a[3] "\t" a[4]
      }
      close(OLDFP)
      while ((getline line < KEYMAP) > 0) {
        n = split(line, a, "\t")
        if (n < 2) continue
        FP[a[1]] = substr(a[2], 1, 12)
      }
      close(KEYMAP)
    }
    $4 == "zero_coverage" {
      h = ($7 != "" ? $7 : $3); d = $6
      key = h "@" d
      if (!(key in FP)) next
      fp = FP[key]
      n = ++CNT[h "\t" d]
      adj = "pending"; rsn = ""; own = ""
      if (fp in OLD) {
        split(OLD[fp], o, "\t")
        adj = o[1]; rsn = o[2]; own = o[3]
      }
      print h "\t" d "\t" $1 "\t" $2 "\t" $9 "\t" $10 "\t" $8 "\t" n "\t" fp "\t" "zero_coverage" "\t" adj "\t" rsn "\t" own
    }
  ' "$AHK_RESULT" \
    | LC_ALL=C sort -t$'\t' -k1,1 -k2,2 -k3,3 >"$AHK_TMP/new_rows.tsv"

  {
    cat <<'EOF'
# API handler「运行时零覆盖」例外台账
#
# ★ 口径：「运行时零覆盖」= coverprofile 里该 handler 函数体范围内
#   **没有一个语句块的执行次数 > 0**（由 scripts/audit/api-handler-coverage.sh 产出）。
#   ★ 它**不是**「测试文件里没出现过这个名字」。
# ★ ★ zero_coverage 是【下界】：审计器判不了的（undetermined）那一类**可能已被覆盖**。
#
# ★ verdict 列       = **机器判定**（本台账只收 zero_coverage ⇒ 恒为 zero_coverage）
# ★ adjudication 列  = **人工裁决**（工具对**新**项写初稿态 `pending`；
#                       门禁止 `pending` ⇒ 交付态必须逐条裁决）
# ★ reason 列        = **人工裁决的理由 / 原因分类**（人工写，**工具不许自动生成**）
# ★ owner 列         = 归属包 scope（admin/ 等）
#
# ★ adjudication 取值域（6 类，人工裁决）：
#     needs-unit-test        补单测即可收口（httptest + 假依赖）
#     needs-integration-test 需真 DB / Redis / 上游集成环境
#     logic-migrated         逻辑已迁到别处 ⇒ 改去新位置补测
#     delegating-wrapper     纯委托 ⇒ 测共享实现即可
#     param-validation-only  只需补参数边界/非法输入用例
#     admin-manual-op        人工运维型接口 ⇒ 需真环境 + 人工流程
#
# ★ 主键 = (handler, fingerprint, occ)：fingerprint = sha256(handler@def_file) 前 12 位
#   （稳定、抗行号漂移）；occ = 同 (handler, def_file) 组内按 path 排序的序号。
#
# ★ carry-over：再次运行审计器时，**同 fingerprint 的旧行的人工裁决逐字保留**。
#
# ★ 本轮（首版）的裁决是**一次性人工首判**：按 handler 函数体形态（是否直连 DB/Redis/
#   是否只做参数解析/是否整体委托/是否运维型）逐条归类；个别行的类型归属可能不准
#   （同名方法在包内多见时只能按字段名近似匹配）⇒ 复核时以 def_file 里的源码为准。
EOF
    # ★★★ R157 轮新增：★ 台账头里记下**生成时工具自己的 sha256**与**包集合**。
    #   ★ 目的：★ 让「工具改了但台账没重生成」变成**机器可判**（★ 否则台账会悄悄过期）。
    #   ★★ 为什么写在**注释头**而不是数据列：★ 写数据列 ⇒ ★ 门的解析器要跟着改列数
    #     ⇒ ★★ 一次工具改动就会连带改门 ⇒ ★★ **耦合面反而更大**；★ 写在头里 ⇒ ★ 门只需读一行。
    #   ★★ 诚实边界：★ 本行只能保证「**工具没变**」；★ 「**代码变了**」也会让覆盖度变，
    #     ★★ 而那需要重跑 go test 才能知道 ⇒ ★★ **代码改过必须手动重生成台账**（★ 命令见下）。
    printf '# gen_tool_sha=%s\n' "$(ahk_file_sha256 "$AHK_SELF")"
    printf '# gen_pkgs=%s\n' "${AHK_PKGS}"
    printf '# gen_cover_sha=%s\n' "$(ahk_file_sha256 "$AHK_COVER")"
    # ★★★ R157 第二轮：★ 再记**推导出来的定义包**与**跨包覆盖口径**。
    #   ★ 为什么：★ 「覆盖度读数」的口径不止一个参数（★ 包集、★ coverpkg、★ 合并去重）
    #     ⇒ ★★ 少了任何一个，★ 数字就不可复现 ⇒ ★★ 台账必须自带口径。
    #   ★ ★★ 「只记事实」原则（★ R157 轮的教训）：★ 头部**不许**写「看起来对但其实
    #     不是这样」的值 ⇒ ★ `--cover`（外部提供 profile）时**不许**写成
    #     「联合覆盖」⇒ ★★ 那正是「撒谎的台账比没有台账更坏」。
    if [ -n "${AHK_COVERPKG:-}" ]; then
      printf '# gen_cover_mode=joint\n'
      # ★ ★ 必须**同时**写 coverpkg 本身（★ R157 轮自测时漏了 ⇒ ★ 门立刻报
      #   「台账头缺 `# gen_coverpkg=`」⇒ ★★★ **这正是新门该有的作用**：
      #   ★★ 它在第一次跑的时候就把我的实现遗漏抓了出来）。
      printf '# gen_coverpkg=%s\n' "${AHK_COVERPKG}"
    elif [ -n "${AHK_COVER_EXTERNAL:-}" ]; then
      printf '# gen_cover_mode=external\n'
      printf '# gen_coverpkg=（外部提供 profile ⇒ 联合覆盖口径由提供方保证）\n'
    else
      printf '# gen_cover_mode=none\n'
      printf '# gen_coverpkg=（无：各包自跑 ⇒ 跨包执行不可见）\n'
    fi
    printf '# gen_defpkgs=%s\n' "${AHK_DEFPKGS:-（未推导）}"
    printf '# gen_two_pass=%s\n' "${AHK_TWO_PASS}"
    printf 'path\tline\tocc\tmethod\thandler\treg_file\tdef_file\tstmts\tverdict\tadjudication\treason\tevidence\towner\tfingerprint\n'
    awk -F'\t' '
      {
        h = $1; d = $2; p = $3; m = $4; rf = $5; regline = $6; stmts = $7; occ = $8; fp = $9
        verdict = $10; adj = $11; rsn = $12; own = $13
        evid = "coverprofile: " rf " 内 " h " 函数体范围内无执行次数>0 的语句块"
        print p "\t" regline "\t" occ "\t" m "\t" h "\t" rf "\t" d "\t" stmts "\t" verdict "\t" adj "\t" rsn "\t" evid "\t" own "\t" fp
      }
    ' "$AHK_TMP/new_rows.tsv"
  } >"$AHK_TMP/ledger_new.tsv"

  mkdir -p "$(dirname "$AHK_LEDGER")"
  cp "$AHK_TMP/ledger_new.tsv" "$AHK_LEDGER"
  echo "… 台账已写出：${AHK_LEDGER}（零覆盖 $(wc -l <"$AHK_TMP/new_rows.tsv" | tr -d ' ') 条）" >&2
fi

# ★★★ R157 轮补：★ 覆盖不全 ⇒ **退出码非 0**（★ 读数照出，★ 但不许被当成权威读数）。
#   ★ 缘由见「ahk_run_pkgs」里的注释：★ `cmd/gateway` 偶发失败会让读数从
#   ★ 150/349/107 退化成 129/347/130，★ 而工具此前**退出码仍是 0**。
#   ⇒ ★★ 退出码 3 = 「环境/覆盖不全」⇒ ★★ 调用方必须**显式**决定要不要用这份读数。
#   ⇒ ★★ 门（scripts/ursmcheck/api_handler_coverage_gate_test.go）**跑快速模式**
#   ⇒ ★★ 那条路径**不**经过 go test ⇒ ★★ 门不受影响。
if [ -n "${AHK_PKG_FAILED_GLOBAL:-}" ]; then
  echo "★★★ 覆盖不全（失败的包：${AHK_PKG_FAILED_GLOBAL}）⇒ ★ 退出码 3" >&2
  echo "★ ★★ 上面的读数**不可**当成权威读数使用（★ 覆盖面缺 ⇒ ★ 零覆盖下界更不准）。" >&2
  exit 3
fi

exit 0
