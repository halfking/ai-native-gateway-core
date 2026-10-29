#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# extract-admin-routes.sh —— 从**当前源码声明**抽取本仓 HTTP 路由清单（TSV）
#
# WHAT THIS IS
# ------------
# 本仓有一条长期目标：「逐个 API 验证 + 效率检查」。
# 巡检器是 `scripts/audit/api-sweep.sh`（仓内），但它吃的**路由清单**此前只存在于
# **仓外**（/tmp/kx-*.txt）⇒ 别人复现不了。
#
# 而「计数是活事实」（§400 通用式）：本仓的路由一直在长，
# 旧清单 444 条 ⇒ 当前源码重抽 503 条 ⇒ **新增 59、消失 0**
# （新增里含整族 `/api/admin/session-analytics/*`，此前从未被覆盖过）。
#
# ⇒ 所以本脚本把「路由清单」变成**仓内快照 + 防漂移门**：
#
#	scripts/audit/extract-admin-routes.sh     ← 本脚本（生成器）
#	scripts/audit/asset/admin-routes.txt      ← 仓内快照（生成产物，不要手改）
#	scripts/ursmcheck/admin_routes_snapshot_gate_test.go  ← 防漂移门
#
# # ① 口径是什么（★ 必读）
#
# 本脚本抽的是「**源码里的声明**」，不是「运行时注册表」。
#
# 具体口径（逐字）：
#
#   · 形态 A：`mux.HandleFunc("PATH", EXPR)` / `mux.Handle("PATH", EXPR)`
#     —— **接收者必须字面叫 `mux`**（见下面的「已知不注册」第 ④ 条）。
#   · 形态 B（Go 1.22 方法前缀）：`mux.HandleFunc("GET /api/x", EXPR)`
#     ⇒ 方法前缀被**拆出来单独成列**（method 列 = `GET`），
#       path 列只留 `/api/x`。
#   · 没有方法前缀 ⇒ method 列 = **`ANY`**（哨兵值，不是源码里的字面量）。
#   · 只认**首参是 Go 字符串字面量**的调用；首参是常量/变量/拼接的抽不出来
#     （见下面的「已知不注册」第 ③ 条）。
#   · 排除 `_test.go`（测试里的 mux 不是生产面）。
#   · 排除**注释里**的调用（注释不是声明）。脚本按 Go 的字符串/注释状态机
#     逐行剥离注释 ⇒ 注释里的 `mux.HandleFunc(...)` 不会被当成登记。
#
# 输出：TSV，每行
#
#	path <TAB> method <TAB> source_file:line
#
#   · **按 path 排序**（LC_ALL=C，逐字节 ⇒ 与 locale 无关、可复现）。
#   · path 与 method **逐字保留源码里的形态**：不改写路径、不去尾斜杠、
#     不做 URL 编码、不补前缀。
#   · **同名 path 多行登记要保留**：Go 的 ServeMux 允许同路径不同方法
#     （以及不同文件里的重复声明），那是**多条登记**，不是重复行。
#     ⇒ 所以**不要**去重。
#   · `source_file` 是**相对仓根**的路径 ⇒ 快照可跨 checkout 比较。
#
# # ② 与运行时注册已知有偏差（★ 必读：这份清单**不是**运行时注册表）
#
# 「源码声明」≠「实际注册」。下面这些偏差**无法**由 grep/sed/awk 自动识别
# （它们要么是控制流层面的事实，要么是跨文件的常量间接引用），
# 所以在这里**显式登记**，而不是假装脚本能判断。
#
#   ① **飞书族：源码有、线上不注册（★ 3 条）**
#      admin/modules.go:1393-1397 的 `registerFeishuRoutes` 里有
#        /api/admin/feishubot/routing-rules
#        /api/admin/feishubot/routing-rules/
#        /api/admin/feishubot/send-log
#      但**那个注册函数故意没有调用点**（源码里 `routemount: disabled` 注释自证：
#      恢复调用点需要「注册调用点 + 迁移 220 进升级序列 + 新装链已满足」三件一起，
#      缺一件生产会从 **404（诚实的「没这个端点」）变成 500（relation does not exist）**）。
#      ⇒ 这 3 条**在源码清单里、但线上不注册**，实测 404 是「诚实的 404」。
#      ⇒ 巡检它们时看到 404 **不是缺陷**。
#
#   ② **条件注册（★ 取决于运行时配置）**
#      api/dingtalk_callback.go:307 的 `POST /api/webhooks/dingtalk/approval-callback`
#      只在 `DINGTALK_SIGN_SECRET` 非空时才被注册（cmd/gateway/main.go:7646-7657）。
#      ⇒ 源码声明了，线上**可能**没有。
#
#   ③ **常量/变量间接引用（★ 4 条注册抽不出来）**
#      首参不是字面量的注册，本脚本抽不出（它只认字面量）：
#        admin/sanitize_rules_handler.go:20  → "/api/admin/sanitize-rules"
#        admin/sanitize_rules_handler.go:21  → "/api/admin/sanitize-rules/"
#        admin/journal_handlers.go:34         → "/api/admin/dispatch/journal/"
#        cmd/gateway/plugin_apiproxy_init.go:88 → method+" /plugins/{pluginId}/api/{rest...}"
#          （method ∈ {GET,POST,PUT,PATCH,DELETE} ⇒ 运行时是 5 条注册）
#      ⇒ 这几条路径**不出现在本清单里**，但它们**在线上是存在的**。
#
#   ④ **接收者不叫 `mux` 的注册（★ 12 条，不在本清单里）**
#      本仓只有两个**别的** ServeMux 用别的接收者名注册：
#        · cmd/gateway/maintain_proxy.go 的 `compat`（维护页兼容 mux，11 条：
#          /、/maintain、/maintain/、/maintain-assets/、+ 变量 maintainAPIPrefix）
#        · cmd/gateway/main_v2_pipeline.go:471 的 `parent`（v2 pipeline 子 mux，
#          1 条：/v2/，且**只在 LLM_GATEWAY_V2_ENABLED=true 时注册**）
#      ⇒ `/v2/` 是**真实网关路由**，但按上面的口径（接收者字面叫 mux）不收。
#        巡检 /v2/* 前请单独确认该开关是开的。
#
#   ⑤ **不扫的目录（★ 它们不是网关主 mux 的面）**
#        vendor/、node_modules/、.git/        —— 第三方/工具目录
#        tests/                               —— 集成测试用的**假服务器**
#                                                   （tests/local、tests/stress 自建 mux）
#        cmd/gateway-v2/                      —— 独立的 v2 网关二进制
#        cmd/quality-service/                 —— 独立微服务（:8081）
#        cmd/test_quality_api/                —— 开发用小工具（硬编码 DSN）
#        internal/quality/                    —— quality-service **自己**的 mux
#                                                   （网关侧走 internal/handlers 的
#                                                   `/api/quality/` 前缀挂载，不是这里）
#      ⇒ 扫哪些目录不是写死「admin/ 与 cmd/gateway/」，而是**扫全仓**
#        （排除上表）⇒ **新增包里的路由会自动进来**，不会静默漏掉。
#
#   ⑥ **本清单保证不了什么**
#      本门只能保证「**快照 == 当前源码**」。
#      ★ 它**不能**保证「线上真的注册了这条路由」——
#        ①（飞书族）与 ②（条件注册）就是反例。
#        ★ 它也**不是**一次运行时探测：**没有** 404/500/延迟读数。
#
# # ③ 怎么复跑（★ 逐字）
#
#   # 在仓根跑，输出到 stdout（纯 TSV，无杂音）：
#   bash scripts/audit/extract-admin-routes.sh
#
#   # 写到文件（门就是这么调的）：
#   bash scripts/audit/extract-admin-routes.sh --out /tmp/admin-routes.tsv
#
#   # 对**别的 checkout** 跑（★ --root 必须先给绝对路径或相对本脚本的路径）：
#   bash scripts/audit/extract-admin-routes.sh --root /path/to/other/checkout --out /tmp/x.tsv
#
#   # 刷新仓内快照（★ 改完源码后必须做，否则防漂移门会红）：
#   # ★★ 用 --update-snapshot（★ 2026-10-27 合并轮新增 ⇒ 下面这行**不要**再照抄旧的）：
#   bash scripts/audit/extract-admin-routes.sh --update-snapshot
#   #
#   # ★★ 【为什么旧的写法被废弃】★★ 旧写法是：
#   #   bash scripts/audit/extract-admin-routes.sh --out scripts/audit/asset/admin-routes.new \
#   #     && { head -1 scripts/audit/asset/admin-routes.txt; \
#   #          cat scripts/audit/asset/admin-routes.new; } > scripts/audit/asset/admin-routes.txt
#   # ★★ 它的 `>` **先截断**目标文件、`{ … }` **后读** ⇒ ⇒ **口径说明头被整行丢掉**
#   # ★★ ⇒ 5 道 admin-routes 门全红，而红的原因**不指向真正的原因**；
#   # ★★ ⇒ 且即便头没丢，头里的「数据行数=N」也是**旧值** ⇒ 门**照样红**。
#   # ⇒ ⇒ `--update-snapshot` 一次做对三件事：★ 先复制再读头（无截断竞态）、
#   # ⇒ ⇒ ★ 重铸头里的行数（头与数据一致）、★ 临时文件 + mv 原子替换。
#
#   # 跑防漂移门：
#   go test ./scripts/ursmcheck/ -run 'TestAdminRoutes' -count=1 -v
#
# 退出码：
#   0   成功
#   64  用法错误（未知参数 / --root 不存在 / --out 写不进去）
#   70  抽取失败（没有任何登记 —— 量具静默失效）
#
# ★ 纯 shell + 标准工具（grep/sed/awk/sort）：**零第三方依赖**
#   （不许 go run / node / python），★ bash 3.2 兼容
#   （macOS 自带 bash 3.2 ⇒ 不许用 declare -A / mapfile / ${var,,}）。
# ---------------------------------------------------------------------------
set -uo pipefail

SELF="${BASH_SOURCE[0]:-$0}"
SCRIPT_DIR="$(cd "$(dirname "$SELF")" && pwd)"
DEFAULT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

ROOT="$DEFAULT_ROOT"
OUT=""
VERBOSE=0
UPDATE_SNAPSHOT=0

usage() {
	sed -n '2,/^# ----/p' "$SELF" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
	case "$1" in
	--root)
		[ $# -ge 2 ] || { echo "--root 缺参数" >&2; exit 64; }
		ROOT="$2"
		shift 2
		;;
	--root=*)
		ROOT="${1#--root=}"
		shift
		;;
	--out)
		[ $# -ge 2 ] || { echo "--out 缺参数" >&2; exit 64; }
		OUT="$2"
		shift 2
		;;
	--out=*)
		OUT="${1#--out=}"
		shift
		;;
	--update-snapshot)
		UPDATE_SNAPSHOT=1
		shift
		;;
	-v | --verbose)
		VERBOSE=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "未知参数：$1（用 --help 看用法）" >&2
		exit 64
		;;
	esac
done

if [ ! -d "$ROOT" ]; then
	echo "--root 不是目录：$ROOT" >&2
	exit 64
fi
# 仓根自检：有 go.mod 才认（防止把 --root 指到某个子目录上 ⇒ 静默少抽）
if [ ! -f "$ROOT/go.mod" ]; then
	echo "--root 下没有 go.mod，不是仓根：$ROOT" >&2
	exit 64
fi
ROOT="$(cd "$ROOT" && pwd)"

TMP_OUT="$(mktemp "${TMPDIR:-/tmp}/extract-admin-routes.XXXXXX")"
TMP_OUT_RAW="$(mktemp "${TMPDIR:-/tmp}/extract-admin-routes-raw.XXXXXX")"
TMP_LIST="$(mktemp "${TMPDIR:-/tmp}/extract-admin-routes-list.XXXXXX")"
SNAP_HDR=""
SNAP_NEW=""
cleanup() {
	rm -f "$TMP_OUT" "$TMP_OUT_RAW" "$TMP_LIST" \
		${SNAP_HDR:+"$SNAP_HDR"} ${SNAP_NEW:+"$SNAP_NEW"}
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# 扫描范围：全仓（★ 不是写死 admin/ 与 cmd/gateway/ ⇒ 新包的路由自动进来）
# 排除项见头部「已知不注册 ⑤」，每一条都有理由。
# ---------------------------------------------------------------------------
find "$ROOT" \
	\( -path "$ROOT/vendor" \
		-o -path "$ROOT/node_modules" \
		-o -path "$ROOT/.git" \
		-o -path "$ROOT/tests" \
		-o -path "$ROOT/cmd/gateway-v2" \
		-o -path "$ROOT/cmd/quality-service" \
		-o -path "$ROOT/cmd/test_quality_api" \
		-o -path "$ROOT/internal/quality" \) -prune \
	-o -type f -name '*.go' -not -name '*_test.go' \
	-exec grep -l -E '(^|[^A-Za-z0-9_])mux\.Handle(Func)?\(' {} + >"$TMP_LIST"

# ---------------------------------------------------------------------------
# 抽取：逐行剥离 Go 注释（字符串字面量感知）⇒ 注释里的注册不算数；
# 再取 `mux.Handle` / `mux.HandleFunc` 的**字面量首参**；
# 形态 B 的方法前缀拆出来单独成列。
# ---------------------------------------------------------------------------
: >"$TMP_OUT_RAW"
while IFS= read -r f; do
	[ -n "$f" ] || continue
	[ -f "$f" ] || continue
	awk -v root="$ROOT/" '
function strip_code(s,   out, i, n, c, two) {
	# 返回 s 的「代码部分」：注释被去掉，字符串字面量**原样保留**。
	# Go 的字符串/注释状态机：行注释 //、块注释 /* */、"..."（含 \ 转义）、`...`。
	out = ""
	i = 1
	n = length(s)
	while (i <= n) {
		c = substr(s, i, 1)
		two = substr(s, i, 2)
		if (inblock) {
			if (two == "*/") { inblock = 0; i = i + 2 } else { i = i + 1 }
			continue
		}
		if (two == "//") { break }                 # 行注释：后面全是注释
		if (two == "/*") { inblock = 1; i = i + 2; continue }
		if (c == "\"") {
			out = out c
			i = i + 1
			while (i <= n) {
				c = substr(s, i, 1)
				out = out c
				if (c == "\\") {
					if (i + 1 <= n) { out = out substr(s, i + 1, 1) }
					i = i + 2
					continue
				}
				i = i + 1
				if (c == "\"") { break }
			}
			continue
		}
		if (c == "`") {
			out = out c
			i = i + 1
			while (i <= n) {
				c = substr(s, i, 1)
				out = out c
				i = i + 1
				if (c == "`") { break }
			}
			continue
		}
		out = out c
		i = i + 1
	}
	return out
}
BEGIN { inblock = 0 }
{
	code = strip_code($0)
	if (match(code, /(^|[^A-Za-z0-9_])mux\.Handle(Func)?\("[^"]*"/)) {
		m = substr(code, RSTART, RLENGTH)
		q = substr(m, index(m, "\"") + 1)
		q = substr(q, 1, length(q) - 1)
		meth = "ANY"
		path = q
		if (match(q, /^(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS|CONNECT|TRACE) /)) {
			meth = substr(q, 1, RLENGTH - 1)
			path = substr(q, RLENGTH + 1)
		}
		rel = FILENAME
		# 去掉仓根前缀 ⇒ 快照里的 source_file 是**相对仓根**的路径（可跨 checkout 比较）
		if (substr(rel, 1, length(root)) == root) {
			rel = substr(rel, length(root) + 1)
		}
		printf "%s\t%s\t%s:%d\n", path, meth, rel, FNR
	}
}
' "$f" >>"$TMP_OUT_RAW"
done <"$TMP_LIST"

LC_ALL=C sort "$TMP_OUT_RAW" >"$TMP_OUT"
REG_COUNT="$(wc -l <"$TMP_OUT" | tr -d ' ')"
UNIQ_COUNT="$(cut -f1 "$TMP_OUT" | LC_ALL=C sort -u | wc -l | tr -d ' ')"

if [ "$REG_COUNT" -eq 0 ]; then
	echo "抽取到 0 条登记 ⇒ 量具静默失效（口径或扫描范围被改坏了）" >&2
	exit 70
fi

if [ "$UPDATE_SNAPSHOT" -eq 1 ]; then
	# ★★ --update-snapshot：原地刷新仓内快照（★ 2026-10-27 合并轮新增）。
	#
	# ★★ **为什么要有这个模式**：★ 原头部注释里给的「刷新仓内快照」写法是
	# ★★   bash scripts/audit/extract-admin-routes.sh --out .../admin-routes.new \
	# ★★     && { head -1 .../admin-routes.txt; cat .../admin-routes.new; } > .../admin-routes.txt
	# ★★ ⇒ ★★ **这一行是破坏性的**：★ `> .../admin-routes.txt` 会在 `{ … }` 子shell
	# ★★ **读之前**就把目标文件**截断成空** ⇒ ⇒ `head -1` 读到的是**空** ⇒
	# ★★ ⇒ **快照的口径说明头被整行丢掉** ⇒ ⇒ 5 道 admin-routes 门全部红，
	# ★★ ⇒ 而红的原因（"第一行不是 # 开头的口径说明头"）**指向快照内容**，
	# ★★ ⇒ **完全不指向真正的原因**（那行 shell 的重定向顺序）。
	# ★★ ⇒ 而且即便头没丢，★ 头里自述的「数据行数=N」也是**旧值**
	# ★★ ⇒ 门的「头与数据必须一致」断言**照样红** ⇒ ⇒ 照抄文档的人**必然撞红**。
	#
	# ★★ 本模式把三件事一次做对：
	#   ① 先把旧快照**复制**到临时文件再读头（★ 不碰原文件 ⇒ 没有截断竞态）；
	#   ② 把头里的「数据行数=N」**改写成新行数**（★ 门要求头与数据一致）；
	#   ③ 先写临时文件、再 `mv` 覆盖（★ 原子替换 ⇒ 中途失败不会留下半个快照）。
	SNAP_REL="scripts/audit/asset/admin-routes.txt"
	SNAP="$ROOT/$SNAP_REL"
	[ -f "$SNAP" ] || {
		echo "仓内快照不存在：${SNAP}（不能凭空造头 ⇒ 先确认路径）" >&2
		exit 64
	}
	SNAP_HDR="$(mktemp "${TMPDIR:-/tmp}/extract-admin-routes-hdr.XXXXXX")"
	SNAP_NEW="$(mktemp "${TMPDIR:-/tmp}/extract-admin-routes-snap.XXXXXX")"
	# ★ 先复制再读头：★ 目标文件全程不被打开为写 ⇒ 没有「截断在前、读在后」的竞态。
	head -1 "$SNAP" >"$SNAP_HDR"
	# ★★ 2026-10-29 审计轮补：**原子替换会顺手改掉权限位**。
	# ★★ `mktemp` 建出来的临时文件是 0600（仅属主可读写）⇒ 而第 ③ 步是
	# ★★ `mv` 覆盖 ⇒ ⇒ 刷新后的快照变成 0600 ⇒ 与同目录其它产物（0644）不一致
	# ★★ ⇒ CI/其它用户/其它账号跑同一个仓时读不到这个快照。
	# ★★ （git 只跟踪「可执行位」，所以 `git status` 看不出来 ⇒ 静默漂移。）
	# ★★ ⇒ 在替换**之前**记下原权限位，替换**之后**原样还回去。
	SNAP_MODE="$(stat -f '%Lp' "$SNAP" 2>/dev/null || stat -c '%a' "$SNAP" 2>/dev/null || true)"
	case "$SNAP_MODE" in
	'' | *[!0-7]*) SNAP_MODE="" ;;
	esac
	if ! grep -q '数据行数=[0-9]' "$SNAP_HDR"; then
		echo "快照头里没有「数据行数=N」⇒ 不敢替你重铸头（先人工确认口径说明行）" >&2
		rm -f "$SNAP_HDR" "$SNAP_NEW"
		exit 70
	fi
	# ★ 重铸头里的行数：★ 只替换这一处，其余口径说明逐字保留。
	sed "s/数据行数=[0-9][0-9]*/数据行数=$REG_COUNT/" "$SNAP_HDR" >"$SNAP_NEW"
	cat "$TMP_OUT" >>"$SNAP_NEW"
	mv "$SNAP_NEW" "$SNAP"
	# ★ 把替换前记下的权限位还回去（★ `mv` 自 mktemp 会落成 0600）。
	[ -n "$SNAP_MODE" ] && chmod "$SNAP_MODE" "$SNAP"
	rm -f "$SNAP_HDR"
	echo "已刷新仓内快照：${SNAP_REL}（${REG_COUNT} 条登记 / ${UNIQ_COUNT} 条唯一 path）" >&2
	exit 0
fi

if [ -n "$OUT" ]; then
	OUT_DIR="$(dirname "$OUT")"
	[ -d "$OUT_DIR" ] || mkdir -p "$OUT_DIR" || {
		echo "建目录失败：$OUT_DIR" >&2
		exit 64
	}
	cp "$TMP_OUT" "$OUT" || {
		echo "写不进去：$OUT" >&2
		exit 64
	}
else
	cat "$TMP_OUT"
fi

if [ "$VERBOSE" -eq 1 ]; then
	echo "extract-admin-routes: ${REG_COUNT} 条登记 / ${UNIQ_COUNT} 条唯一 path（root=${ROOT}）" >&2
fi

exit 0
