#!/usr/bin/env bash
# verify-v1-retirement-red-gates.sh —— 本轮（2026-10-21）4 条红门修复的变异 harness。
#
# ★ 这道脚本守什么
#
# 本轮在全仓 `go test ./...` 里发现 4 个**既有红门**（origin/main 上就红），根因分两类：
#
#   ① 门的前提随被观测对象一起消失（迁移 890 退役 v1 族）：
#      domains/streaming、internal/sessionv2mirror。
#   ② 门太粗 / 门已过期：scripts/ursmcheck（把正文里的示例误判成真冲突）、
#      sql/schema（真缺陷：bash 4.0+ 的大小写展开在 macOS bash 3.2 上 bad substitution）。
#
# ⇒ 修完不等于修对：每条修复都要**改坏即红**才算数。
#
# ★ 本脚本的五条硬规则（每条都对应一次真实踩坑）
#
#   1. **锚点恰好命中 1 次**：替换前先断言命中数 == 1。
#      （命中 0 次 ⇒ 变异没注入 ⇒「门红」是假的；命中 >1 ⇒ 改的是别的地方。）
#      ★ 计数用 **python 的字面量计数**，不用 `grep -c`：后者按**行**计数
#        ⇒ 多行锚点被拆开分别匹配 ⇒ 命中数虚高（实测报「248 次」而实际 1 处）。
#   2. **编译红不算数**：变异后测试必须能**编译**并跑起来。
#      （编译红只说明代码坏了，不说明判据有牙。）
#   3. **变红的是预期集合之外的测试不算数**：本脚本抓 `--- FAIL: <name>`，
#      并要求失败测试名落在本次变异的**预期集合**里。
#   4. **还原纪律**：按**原路径**做 sha256 逐字校验。
#      ⚠ 绝不用 `git checkout --` 还原 —— 它回到 HEAD，把本轮**修复本身**
#        一起丢掉（2026-10-21 实测踩过：还原后 md5 与变异前不一致，
#        而「校验」读的是同一个错路径 ⇒ 报出假读数）。
#   5. **锚点与替换物用 base64 传参**：shell 里写真实 tab / 多行会踩转义地狱
#      ⇒ 交给仓内 helper scripts/audit/_mutate_helper.py（支持 \t / \n 转义）。
#
# 用法：bash scripts/audit/verify-v1-retirement-red-gates.sh
# ★★ r1047 轮（★ 2026-10-28）：★ 本脚本【改树 ⇒ 跑 go test ⇒ 还原】⇒ ⇒ 树每变一次
# ★★ ⇒ ⇒ 编译产物的 action ID 就变 ⇒ ⇒ 旧的【永远不会再被命中】⇒ ⇒ 纯垃圾 ⇒ ⇒
# ★★ ⇒ ⇒ 绝不能落在用户的全局缓存里 ⇒ ⇒ 指向【专用缓存】（★ 目录约定与 6 GB 上限见
# ★★ `scripts/lib/gocache.sh`；★ 理由与实测读数见 `internal/gocache` 的包注释）。
export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"


set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 1
MUT_HELPER="$ROOT/scripts/audit/_mutate_helper.py"
export PATH="$HOME/sdk/go/bin:$PATH"

PASS=0
FAIL=0
FAILED_LIST=""

TMPDIR_MUT="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_MUT"' EXIT

say() { printf '%s\n' "$*"; }

# ── 还原（按原路径，sha256 逐字校验）────────────────────────────────────
# restore_file <相对路径> <备份文件>
restore_file() {
    local rel="$1" bak="$2" a b
    cp "${bak}" "${rel}"
    a="$(shasum -a 256 "${bak}" | awk '{print $1}')"
    b="$(shasum -a 256 "${rel}" | awk '{print $1}')"
    if [ "${a}" != "${b}" ]; then
        say "    ❌ 还原不一致：${rel}（before=${a} after=${b}）"
        return 1
    fi
    return 0
}

# ── 单点变异：<文件> <锚点> <替换为> [模式] ───────────────────────────
# 模式默认 replace（整块字面量替换）；replace_line = 替换「包含锚点的那一整行」。
# 锚点与替换物都用 base64 传参 ⇒ shell 侧不需要任何转义；
# helper 支持 \t / \n 转义序列（用于表达 tab 与多行）。
mutate_once() {
    local rel="$1" needle="$2" repl="$3" mode="${4:-replace}"
    local nb rb n
    nb="$(printf '%s' "${needle}" | base64 | tr -d '\n')"
    rb="$(printf '%s' "${repl}" | base64 | tr -d '\n')"
    n="$(python3 "${MUT_HELPER}" count "${rel}" "$nb")"
    if [ "${n}" != "1" ]; then
        say "    ❌ 锚点在 ${rel} 里命中 ${n} 次（必须恰好 1 次）⇒ 拒绝注入变异"
        return 1
    fi
    if ! python3 "${MUT_HELPER}" "${mode}" "${rel}" "$nb" "$rb" >/dev/null; then
        say "    ❌ 变异注入失败（${mode}）于 ${rel}"
        return 1
    fi
    return 0
}

# ── 跑一个包并检查「红在预期集合里」────────────────────────────────────
# expect_red <标签> <go 包路径> <预期失败测试名...>
# 环境变量 RUN_FILTER 可选：限定 -run 正则（只跑相关测试，快）。
#   ★ 仍要求「失败测试名落在预期集合里」⇒ 限定 -run 不削弱纪律，
#     它只缩小**被观察的集合**。
expect_red() {
    local label="$1" pkg="$2"
    shift 2
    local expected="$*"
    local out rc got name ok=0
    if [ -n "${RUN_FILTER:-}" ]; then
        out="$(go test "${pkg}" -run "${RUN_FILTER}" -count=1 -v 2>&1)"
    else
        out="$(go test "${pkg}" -count=1 -v 2>&1)"
    fi
    rc=$?
    if [ "${rc}" -eq 0 ]; then
        say "    ❌ ${label}：测试**全绿** ⇒ 变异没被抓住 ⇒ 这道修复没有牙"
        FAIL=$((FAIL + 1))
        FAILED_LIST="$FAILED_LIST
  - ${label}"
        return 1
    fi
    # 编译红不算数：输出里出现 build failed / undefined 等 ⇒ 只说明代码坏了。
    if printf '%s' "${out}" | grep -qE 'build failed|undefined:|cannot use|syntax error|declared and not used'; then
        say "    ❌ ${label}：红在**编译**上（不是断言红）⇒ 不算数"
        FAIL=$((FAIL + 1))
        FAILED_LIST="$FAILED_LIST
  - ${label}（编译红）"
        return 1
    fi
    got="$(printf '%s' "${out}" | sed -n 's/^--- FAIL: \([A-Za-z0-9_]*\).*/\1/p' | sort -u)"
    for name in ${expected}; do
        if printf '%s\n' "${got}" | grep -qxF "${name}"; then
            ok=1
            break
        fi
    done
    if [ "${ok}" -ne 1 ]; then
        say "    ❌ ${label}：红了，但失败测试不在预期集合 [${expected}] 里"
        say "       实际失败：$(printf '%s' "${got}" | tr '\n' ' ')"
        FAIL=$((FAIL + 1))
        FAILED_LIST="$FAILED_LIST
  - ${label}（变红的是别的测试）"
        return 1
    fi
    say "    ✅ ${label}：如期变红（${expected}）"
    PASS=$((PASS + 1))
    return 0
}

# ── 跑一个包并检查「绿」──────────────────────────────────────────────
expect_green() {
    local label="$1" pkg="$2"
    local rc
    if [ -n "${RUN_FILTER:-}" ]; then
        go test "${pkg}" -run "${RUN_FILTER}" -count=1 >/dev/null 2>&1
        rc=$?
    else
        go test "${pkg}" -count=1 >/dev/null 2>&1
        rc=$?
    fi
    if [ "${rc}" -eq 0 ]; then
        say "    ✅ ${label}：全绿"
        PASS=$((PASS + 1))
        return 0
    fi
    say "    ❌ ${label}：全绿断言失败 ⇒ 修复后基线就是红的"
    FAIL=$((FAIL + 1))
    FAILED_LIST="$FAILED_LIST
  - ${label}"
    return 1
}

# ══════════════════════════════════════════════════════════════════════
# 基线：4 个包应当全绿
# ══════════════════════════════════════════════════════════════════════
say "=== 基线：4 个包应当全绿 ==========================================="
RUN_FILTER="TestLessonsFileHasNoConflictMarkers|TestFindConflictMarkersSelfCheck" expect_green "基线 · scripts/ursmcheck" "./scripts/ursmcheck/"
RUN_FILTER="TestShellScriptsAvoidBash4OnlyFeatures|TestBash4FeatureScannerSelfCheck" expect_green "基线 · sql/schema" "./sql/schema/"
RUN_FILTER="TestLogAudioUsage" expect_green "基线 · domains/streaming" "./domains/streaming/"
RUN_FILTER="TestAbandonedTurn|TestFinalSuccess" expect_green "基线 · internal/sessionv2mirror" "./internal/sessionv2mirror/"

# ══════════════════════════════════════════════════════════════════════
# M1 · LESSONS 冲突标记门（门太粗 ⇒ 假红）
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M1 · LESSONS 冲突标记门：插入真冲突标记 ⇒ 门必须红 ============="
cp LESSONS.md "$TMPDIR_MUT/LESSONS.md.bak"
if mutate_once "LESSONS.md" \
    "# 项目经验队列（LESSONS）" \
    "$(printf '# 项目经验队列（LESSONS）\n<<<<<<< HEAD\n冲突内容\n=======\n>>>>>>> abc123 (msg)')"; then
    RUN_FILTER="TestLessonsFileHasNoConflictMarkers|TestFindConflictMarkersSelfCheck" expect_red "M1 LESSONS 门" "./scripts/ursmcheck/" "TestLessonsFileHasNoConflictMarkers"
    restore_file "LESSONS.md" "$TMPDIR_MUT/LESSONS.md.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "LESSONS.md" "$TMPDIR_MUT/LESSONS.md.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M2 · sync-objects.sh 的 bash 4 特性（真缺陷 ⇒ 平台依赖的假绿）
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M2 · sync-objects.sh：改回 bash 4 的大小写展开 ⇒ 静态门必须红 ==="
cp deploy/sql/sync-objects.sh "$TMPDIR_MUT/sync-objects.sh.bak"
if mutate_once "deploy/sql/sync-objects.sh" '# ${obj_title}' '# ${obj_type^}'; then
    RUN_FILTER="TestShellScriptsAvoidBash4OnlyFeatures|TestBash4FeatureScannerSelfCheck" expect_red "M2 bash4 静态门" "./sql/schema/" "TestShellScriptsAvoidBash4OnlyFeatures"
    restore_file "deploy/sql/sync-objects.sh" "$TMPDIR_MUT/sync-objects.sh.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "deploy/sql/sync-objects.sh" "$TMPDIR_MUT/sync-objects.sh.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M3 · 审计脚本里的 mapfile（本地审计在 macOS 上根本跑不了）
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M3 · run-integration-gate.sh：改回 mapfile ⇒ 静态门必须红 ======="
cp scripts/audit/run-integration-gate.sh "$TMPDIR_MUT/rig.sh.bak"
if mutate_once "scripts/audit/run-integration-gate.sh" \
    "    gap_actual=()" \
    "$(printf '    gap_actual=()\n    mapfile -t gap_actual < <(true)')"; then
    RUN_FILTER="TestShellScriptsAvoidBash4OnlyFeatures|TestBash4FeatureScannerSelfCheck" expect_red "M3 bash4 静态门" "./sql/schema/" "TestShellScriptsAvoidBash4OnlyFeatures"
    restore_file "scripts/audit/run-integration-gate.sh" "$TMPDIR_MUT/rig.sh.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "scripts/audit/run-integration-gate.sh" "$TMPDIR_MUT/rig.sh.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M4 · audio usage no-op 不得再有 INSERT
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M4 · audio_usage_log.go：加回 INSERT ⇒ 门必须红 ==============="
cp domains/streaming/audio_usage_log.go "$TMPDIR_MUT/audio.go.bak"
if mutate_once "domains/streaming/audio_usage_log.go" \
    'func (l *pgAudioUsageLogger) LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64) {' \
    'func (l *pgAudioUsageLogger) LogAudioUsage(ctx context.Context, tenantID, requestID, model string, seconds float64, credits int64) {\n\t_, _ = l.pool.Exec(ctx, "INSERT INTO _request_logs_hot (a) VALUES ($1)", tenantID)' \
    replace_line; then
    RUN_FILTER="TestLogAudioUsage" expect_red "M4 no-op 无写语句" "./domains/streaming/" "TestLogAudioUsageNoOpHasNoWriteStatement"
    restore_file "domains/streaming/audio_usage_log.go" "$TMPDIR_MUT/audio.go.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "domains/streaming/audio_usage_log.go" "$TMPDIR_MUT/audio.go.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M5 · audio usage no-op 必须留痕（不许静默丢弃）
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M5 · audio_usage_log.go：把日志调用换成取值 ⇒ 门必须红 ========"
cp domains/streaming/audio_usage_log.go "$TMPDIR_MUT/audio.go.bak2"
if mutate_once "domains/streaming/audio_usage_log.go" \
    '\tslog.Debug("audio usage log: v1 family retired, audio-seconds observation has no session-family home",\n\t\t"request_id", requestID,\n\t\t"model", model,\n\t\t"seconds", seconds)' \
    '\t_ = slog.Default'; then
    RUN_FILTER="TestLogAudioUsage" expect_red "M5 no-op 留痕" "./domains/streaming/" "TestLogAudioUsageNoOpLeavesATrace"
    restore_file "domains/streaming/audio_usage_log.go" "$TMPDIR_MUT/audio.go.bak2" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "domains/streaming/audio_usage_log.go" "$TMPDIR_MUT/audio.go.bak2" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M6 · abandoned pad 的触发源必须仍然退役
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M6 · client.go：加回 T0Missing 设置点 ⇒ 门必须红 ==============="
cp domains/hooks/observability/telemetry/client.go "$TMPDIR_MUT/client.go.bak"
if mutate_once "domains/hooks/observability/telemetry/client.go" \
    'func inferRequestTypeV32(entry *RequestLogEntry) string {' \
    'func inferRequestTypeV32(entry *RequestLogEntry) string {\n\t// mutation: trigger source came back\n\t_ = func() bool { e := RequestLogEntry{}; e.T0Missing = true; return e.T0Missing }()' \
    replace_line; then
    RUN_FILTER="TestAbandonedTurn" expect_red "M6 abandoned 触发源退役" "./internal/sessionv2mirror/" "TestAbandonedTurnTriggerSourceIsRetired"
    restore_file "domains/hooks/observability/telemetry/client.go" "$TMPDIR_MUT/client.go.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "domains/hooks/observability/telemetry/client.go" "$TMPDIR_MUT/client.go.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M7 · abandoned pad 的退役登记不得被抹掉（**机器可读**那一份）
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M7 · abandoned_turn.go：改写退役标记常量 ⇒ 门必须红 =========="
cp internal/sessionv2mirror/abandoned_turn.go "$TMPDIR_MUT/pad.go.bak"
if mutate_once "internal/sessionv2mirror/abandoned_turn.go" \
    'const abandonedTurnTriggerRetired = "v1-family-retired@2026-10-20"' \
    'const abandonedTurnTriggerRetired = "v1-family-retired@MUTATED"'; then
    RUN_FILTER="TestAbandonedTurn" expect_red "M7 abandoned 退役登记" "./internal/sessionv2mirror/" "TestAbandonedTurnTriggerSourceIsRetired"
    restore_file "internal/sessionv2mirror/abandoned_turn.go" "$TMPDIR_MUT/pad.go.bak" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "internal/sessionv2mirror/abandoned_turn.go" "$TMPDIR_MUT/pad.go.bak" || true
fi

# ══════════════════════════════════════════════════════════════════════
# M8 · final success 认领不得回到 copy 形态
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== M8 · client.go：加回 fallback copy 形态 ⇒ 门必须红 ============"
cp domains/hooks/observability/telemetry/client.go "$TMPDIR_MUT/client.go.bak2"
if mutate_once "domains/hooks/observability/telemetry/client.go" \
    '\tif shouldClaimFinalSuccess(entry) {\n\t\tentry.FinalSuccessClaimed = true\n\t}\n\tif c.outboxWriter != nil' \
    '\tif shouldClaimFinalSuccess(entry) {\n\t\tfallback := *entry\n\t\tfallback.FinalSuccessClaimed = true\n\t\tentry.FinalSuccessClaimed = fallback.FinalSuccessClaimed\n\t}\n\tif c.outboxWriter != nil'; then
    RUN_FILTER="TestFinalSuccess" expect_red "M8 final success 认领形态" "./internal/sessionv2mirror/" "TestFinalSuccessClaimIsSetOnTheEntry"
    restore_file "domains/hooks/observability/telemetry/client.go" "$TMPDIR_MUT/client.go.bak2" || FAIL=$((FAIL + 1))
else
    FAIL=$((FAIL + 1))
    restore_file "domains/hooks/observability/telemetry/client.go" "$TMPDIR_MUT/client.go.bak2" || true
fi

# ══════════════════════════════════════════════════════════════════════
# 收尾：还原后必须重新全绿
# ══════════════════════════════════════════════════════════════════════
say ""
say "=== 收尾：还原后 4 个包必须重新全绿 =============================="
RUN_FILTER="TestLessonsFileHasNoConflictMarkers|TestFindConflictMarkersSelfCheck" expect_green "还原后 · scripts/ursmcheck" "./scripts/ursmcheck/"
RUN_FILTER="TestShellScriptsAvoidBash4OnlyFeatures|TestBash4FeatureScannerSelfCheck" expect_green "还原后 · sql/schema" "./sql/schema/"
RUN_FILTER="TestLogAudioUsage" expect_green "还原后 · domains/streaming" "./domains/streaming/"
RUN_FILTER="TestAbandonedTurn|TestFinalSuccess" expect_green "还原后 · internal/sessionv2mirror" "./internal/sessionv2mirror/"

say ""
say "==================================================="
say "通过 ${PASS} / 失败 ${FAIL}"
if [ "${FAIL}" -ne 0 ]; then
    say "失败项：$FAILED_LIST"
    exit 1
fi
say "全部变异都如期变红，且还原后逐字一致。"
