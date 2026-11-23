#!/usr/bin/env bash
# 变异 harness —— 跨实例候选缓存失效通道（2026-11-11 缓存与路由状态即时同步轮）
#
# 目的：证明 provider/candidate_cache_invalidation_test.go 的守卫**真的有牙**。
#
# ★ 为什么这个守卫必须做变异验证（而不是「测试绿了就算数」）：
#   本守卫钉的是**跨进程语义**（A 翻判定 ⇒ B 的缓存自己被清掉）。
#   这类守卫最常见的退化形态是「测的其实是自己那一半」——
#   比如断言只覆盖了本地失效，广播那一半根本没被执行 ⇒ 测试照样绿。
#   ⇒ 只有「把广播掐掉 ⇒ 门必须红」才能证明它真的跨了进程。
#
# 三件套纪律（照抄仓内既有 harness，见 scripts/audit/ir-gemini-only-loss-mutation.sh）：
#   ① 变异**生效**的证据：目标文件 SHA 变化（独立于门）
#   ② 门**变红**：目标测试用例 FAIL
#   ③ 还原后 **SHA 复核 + 正向复跑**
#
# ★ 纪律：编译红**不算**判据有牙（只说明代码坏了）⇒ 单独分类。
#
# ★★ **不要与手工改树并发跑本 harness**（★ 本轮实测踩到）★★：
#   本 harness 是「改树 ⇒ 跑 go test ⇒ 还原」形态 ⇒ ⇒ 若**同时**有人在同一棵
#   树上改**同一个文件**，本 harness 会读到**半改状态** ⇒ ⇒ 报出
#   「编译红」这种**假红**（★ 2026-11-11 实测：手工复核 M8 变异时与
#   `make guards-candidate-cache-sync` 重叠 ⇒ M8 被判「编译红」）⇒ ⇒
#   ★ 纪律：★ 本 harness 运行期间**不要**手工改 provider/ 与 bg/ 下的文件 ★。
#
# ★★★ 2026-11-23 加固轮：**「纪律」改成「机器强制」** ★★★
#   ★ 起因（★ 实测，不是推断 ★）：★ 本轮第一次跑本 harness 时，
#     基线文件**已经带着别的变异**（M5 的替换物：`invalidateAllCandidateCacheLocal()`
#     那一行被删）⇒ ⇒ 实测读数：
#       ① M5 臂报「锚点命中 0 次」⇒ ★ **锚点本来是对的**（对着已提交树实测命中 1 次）
#          ⇒ 是**基线脏**导致锚点找不到 ⇒ ⇒ **那条臂根本没跑到** ⇒ 门白丢一条判据；
#       ② M2/M3/M6/M7/M8 五条臂的红因里**混进了 `AllPayloadDropsEverything`**
#          ⇒ ⇒ 那些臂与该用例**无关** ⇒ ⇒ 红的归因被污染 ⇒ ⇒ 臂的判定不可信；
#       ③ 「还原后 SHA 复核一致」**照样打印 ✅** ⇒ ⇒ 因为它比的是
#          **自己的备份**（备份本身就是脏的）⇒ ⇒ **自证式复核** ⇒
#          ⇒ 门在树被留坏的情况下报「还原成功」；
#       ④ 「还原后正向复跑失败」⇒ ⇒ 树最终被留在了**编译得过但行为坏**的状态。
#   ★ ⇒ **根因**：★ 本 harness 的「还原复核」是**相对自己的基线**的，
#     而基线可以脏 ⇒ ⇒ 于是「基线脏」这件事**自己永远发现不了**。
#   ★ ⇒ **修法（本轮落地）**：★
#     ① **前置判据**：★ 三个变异目标文件必须**与已提交状态逐字一致**，
#        否则**拒绝开跑**（一个字节都不改）⇒ ⇒ 基线脏 ⇒ 直接报红并说清原因；
#     ② **还原复核改为对已提交状态**：★ 还原后不再只比自己的备份，
#        而是要求**工作树与 HEAD 一致**⇒ ⇒ 「还原成功」变成**绝对陈述**；
#     ③ **并发互斥**：★ 用 `mkdir` 原子锁挡住**第二个实例**⇒ ⇒
#        「两个 harness 交错改同一棵树」这个根因被机器挡住（不再只靠纪律）；
#     ④ **基线必须绿**：★ 先跑一次**未变异**的基线用例；不绿 ⇒ **拒绝开跑** ⇒ ⇒
#        「一个包编译红 ⇒ 八条臂全部编译红 ⇒ 判定全部作废且归因指错方向」
#        这类噪声被前置成**一条**明确的红（★ 实测：并行会话正把 `bg/probe_http.go`
#        改到一半 ⇒ `./bg` build failed ⇒ 八条臂全红且指错方向）。
#
# 用法：bash scripts/audit/provider-candidate-cache-invalidation-mutation.sh

set -uo pipefail

# 变异 harness 是「改树 ⇒ 跑 go test ⇒ 还原」形态 ⇒ 必须指向专用构建缓存
# （gocache_script_family_guard 门判据①）。
export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
TARGET_FILE="provider/candidate_cache_invalidation.go"
BACKUP_DIR="$(mktemp -d /tmp/provider-cand-inval-mut.XXXXXX)"
TEST_PATTERN="TestCandidateCacheInvalidation|TestModelProbeInvalidateCandidateCacheHookSemantics|TestModelProbeApplyResult"
TEST_PKGS="./provider ./bg"

# ★★ 三个变异目标文件（★ 前置判据与还原复核都只看这三个，不看整棵树）★★
#   ⇒ ⇒ 并行会话改**别的**文件（web/ 等）**不拦**本 harness ⇒ ⇒ 门不误伤。
MUTATION_TARGETS=("$TARGET_FILE" "provider/client.go" "bg/model_probe.go")

# ── 并发互斥（★ `mkdir` 是原子的 ⇒ 跨平台（macOS/Linux）都成立）────────
LOCK_DIR="${TMPDIR:-/tmp}/provider-cand-inval-mutation.lock"
LOCK_HELD=0

# ── 前置判据①：三个变异目标必须与**已提交状态**逐字一致 ───────────────
# ★ 用 `git diff --quiet HEAD -- <paths>`（★ 退出码判定、★ 不产出管道）
#   而不是 `git status --porcelain`：★ 后者按仓内「枚举面转义契约门」的要求
#   必须带 -z，而带 -z 的输出含 NUL（bash 变量吞 NUL）⇒ ⇒ 改用退出码判定，
#   既无管道也不碰 NUL ⇒ ⇒ 不给 pipefail 台账新增候选面。
baseline_is_committed() {
    git diff --quiet HEAD -- "${MUTATION_TARGETS[@]}"
}

# ── 前置判据②：并发互斥（★ 挡住第二个实例）────────────────────────
acquire_lock() {
    if mkdir "$LOCK_DIR" 2>/dev/null; then
        echo $$ > "$LOCK_DIR/pid"
        LOCK_HELD=1
        return 0
    fi
    # ★ 陈旧锁（★ 上一个实例被 kill ⇒ EXIT trap 没跑到）⇒ 判活性后接管 ★
    local owner=""
    if [[ -f "$LOCK_DIR/pid" ]]; then
        owner="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
    fi
    if [[ -n "$owner" ]] && kill -0 "$owner" 2>/dev/null; then
        return 1
    fi
    rm -rf "$LOCK_DIR" 2>/dev/null || true
    if mkdir "$LOCK_DIR" 2>/dev/null; then
        echo $$ > "$LOCK_DIR/pid"
        LOCK_HELD=1
        return 0
    fi
    return 1
}

cleanup() {
    if [[ -f "$BACKUP_DIR/candidate_cache_invalidation.go" ]]; then
        cp "$BACKUP_DIR/candidate_cache_invalidation.go" "$TARGET_FILE"
    fi
    if [[ -f "$BACKUP_DIR/client.go" ]]; then
        cp "$BACKUP_DIR/client.go" "provider/client.go"
    fi
    if [[ -f "$BACKUP_DIR/model_probe.go" ]]; then
        cp "$BACKUP_DIR/model_probe.go" "bg/model_probe.go"
    fi
    rm -rf "$BACKUP_DIR"
    # ★ 释放互斥锁（★ 只在自己真的拿到过锁时才删 ⇒ 不误删别人的锁）★
    if [[ "$LOCK_HELD" -eq 1 ]]; then
        rm -rf "$LOCK_DIR" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ★★ 前置判据②的执行（★ 在**碰任何文件之前**）★★
if ! acquire_lock; then
    echo "❌ 已有另一个 provider-candidate-cache 变异 harness 实例在跑（锁：${LOCK_DIR}）"
    echo "   ⇒ 本 harness 是「改树 ⇒ 跑 go test ⇒ 还原」形态 ⇒ 两个实例交错改同一棵树"
    echo "   ⇒ 会互相污染基线（★ 实测：基线被污染时，门会报「还原成功」而树是坏的）"
    echo "   ⇒ ⇒ **拒绝并发**：本次一个字节都不改，退出码 1。"
    exit 1
fi

# ★★ 前置判据①的执行（★ 在**碰任何文件之前**）★★
# ★ HEAD 必须可解析（★ 否则 `git diff --quiet HEAD` 的失败原因会被误读成「树脏」）
if ! git rev-parse --verify --quiet HEAD >/dev/null; then
    echo "❌ 本仓没有可解析的 HEAD ⇒ 无法判定「基线是否与已提交状态一致」⇒ **拒绝开跑**"
    echo "   ⇒ 本次一个字节都不改，退出码 1。"
    exit 1
fi
if ! baseline_is_committed; then
    echo "❌ 三个变异目标文件与已提交状态（HEAD）不一致 ⇒ 基线是脏的 ⇒ **拒绝开跑**"
    echo "   目标：${MUTATION_TARGETS[*]}"
    echo "   ⇒ 原因：★ 基线脏 ⇒ ① 锚点可能对不上 ⇒ 那条臂根本没跑到（门白丢一条判据）；"
    echo "            ② 各臂的红因被污染 ⇒ 臂的判定不可信；"
    echo "            ③ 「还原后 SHA 复核」比的是自己的备份 ⇒ **自证式复核** ⇒"
    echo "               门会在树被留坏的情况下报「还原成功」。"
    echo "   ⇒ 本次一个字节都不改，退出码 1。请先清理工作树（git checkout -- <上面三个文件>）。"
    exit 1
fi

# ★★ 前置判据③：**基线必须是绿的**（★ 未变异状态下那批用例就该全过）★★
# ★ 为什么必须加这一条（★ 2026-11-23 实测撞到）★★
#   ★ 实测：并行会话正在改 `bg/probe_http.go`（**改到一半、编译不过**）
#     ⇒ ⇒ `./bg` 整包 build failed
#     ⇒ ⇒ 本 harness 的每条臂都跑的是 `go test ./provider ./bg`
#       ⇒ ⇒ **一个包编译红 ⇒ 八条臂全部「编译红」**
#       ⇒ ⇒ 而「编译红」按纪律**不算判据有牙** ⇒ ⇒ ⇒ 八条臂的判定**全部作废**
#       ⇒ ⇒ 且报错指向「锚点/替换物选错」⇒ ⇒ **归因完全指错方向**
#       ⇒ ⇒ 「还原后正向复跑失败」也一样 ⇒ ⇒ 整门红得莫名其妙。
#   ★ ⇒ ⇒ **判据有牙的前提是「被测的那批用例在未变异时是绿的」**
#     ⇒ ⇒ 所以：**先跑一次未变异的基线**；不绿 ⇒ **拒绝开跑**，
#       并把原始输出打出来（★ 让人一眼看到是哪个包编译红／哪条用例本来就红）。
#   ★ ★★ 与前置判据①的区别（★ 别混）：
#     ① 管的是「**被改的那三个文件**是不是已提交状态」
#     ③ 管的是「**被测的那两个包**此刻能不能编译／基线用例是不是本来就红」
#     ⇒ ⇒ 两个都要有：① 防「脏基线」，③ 防「毒基线」。
if ! BASELINE_OUT="$(go test $TEST_PKGS -run "$TEST_PATTERN" -count=1 -timeout 300s 2>&1)"; then
    echo "❌ **基线（未变异）就不绿** ⇒ 本次所有臂的判定都不可信 ⇒ **拒绝开跑**"
    echo "   被测包：${TEST_PKGS[*]}"
    echo "   ⇒ 原因：★ 判据有牙的前提是「被测用例在未变异时是绿的」"
    echo "     ⇒ ⇒ 一个包编译红 ⇒ 八条臂**全部**判「编译红」⇒ ⇒ 判定全部作废且归因指错方向"
    echo "     ⇒ ⇒ 典型成因：**并行会话正在改这些包里的文件、且改到一半**"
    echo "   ⇒ 本次一个字节都不改，退出码 1。"
    echo "───── 基线原始输出（诊断用）─────"
    echo "$BASELINE_OUT" | head -25
    echo "──────────────────────────────────"
    exit 1
fi
echo "✅ 基线（未变异）正向复跑通过 ⇒ 判据有牙的前提成立"

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

# ★ 可移植 SHA-256（macOS 用 shasum，Linux 容器用 sha256sum）。
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# 变异臂（tab 分隔：id / 目标文件 / 锚点 / 替换物 / 说明）
#   ★ 每条锚点必须**恰好命中 1 次**（helper 会拒绝并报命中数）——
#     命中数不对 = 变异没落到点上 = 判据无效（不是「门没牙」）。
#   ★ 目标文件逐臂指定：有的缺陷在**桥**里（candidate_cache_invalidation.go），
#     有的在**调用点**里（client.go 的广播调用）⇒ 两条都要能变异到。
read -r -d '' ARMS <<'EOF'
M1	provider/client.go	publishCandidateCacheInvalidation(credentialID)	_ = credentialID	掐掉广播 ⇒ 跨实例那一半失效
M2	provider/candidate_cache_invalidation.go	b.client.invalidateCandidateCacheLocal(credentialID)	_ = credentialID	订阅端不做本地失效 ⇒ 收到事件也不清
M3	provider/candidate_cache_invalidation.go	b.client.invalidateCandidateCacheLocal(credentialID)	publishCandidateCacheInvalidation(credentialID)	订阅端回环 ⇒ 一条事件变成无限广播
M4	provider/candidate_cache_invalidation.go	if !ok {\n\t\t\t\t// 脏载荷：跳过该条，**不阻断**通道。\n\t\t\t\tcontinue\n\t\t\t}	if !ok {\n\t\t\t\treturn\n\t\t\t}	脏载荷打断通道 ⇒ 后续合法事件永远不生效
M5	provider/candidate_cache_invalidation.go	if credentialID == 0 {\n\t\t\t\tb.client.invalidateAllCandidateCacheLocal()\n\t\t\t\tcontinue\n\t\t\t}	if credentialID == 0 {\n\t\t\t\tcontinue\n\t\t\t}	「整库失效」载荷被当成普通事件丢弃
M6	bg/model_probe.go	//   ⇒ ⇒ 否则已被判死的桶还能再被选中最多 30s（candidateCacheTTL）。\n\t\tr.invalidateCandidateCacheForCredential(t.CredentialID)	//   ⇒ ⇒ 否则已被判死的桶还能再被选中最多 30s（candidateCacheTTL）。	legacy 栈：broken_confirmed 臂不触发候选缓存失效
M7	bg/model_probe.go	//   ⇒ ⇒ 否则已被判死的桶还能再被选中最多 30s（candidateCacheTTL）。\n\t\tr.invalidateCandidateCacheForCredential(t.CredentialID)	//   ⇒ ⇒ 否则已被判死的桶还能再被选中最多 30s（candidateCacheTTL）。\n\t\tr.invalidateCandidateCacheForCredential(0)	legacy 栈：失效调用拿错凭据 ID（0）
M8	bg/model_probe.go	if r == nil || r.invalidateCandidateCache == nil || credentialID == 0 {	if false {	legacy 栈：失效钩子的 nil 守卫被摘掉
EOF

PASS=0
FAIL=0
FAILED_ARMS=""
# ★ 中止标志（★ 「编译红非本臂所致」时提前收摊 ⇒ 不再产出后面的假红）★
ABORT_SWEEP=0

echo "=============================================================="
echo " 变异 harness —— 跨实例候选缓存失效通道"
echo " 目标文件: $TARGET_FILE"
echo "=============================================================="

cp "$TARGET_FILE" "$BACKUP_DIR/candidate_cache_invalidation.go"
cp "provider/client.go" "$BACKUP_DIR/client.go"
cp "bg/model_probe.go" "$BACKUP_DIR/model_probe.go"
BASE_SHA=$(sha256_of "$TARGET_FILE")
BASE_SHA_CLIENT=$(sha256_of "provider/client.go")
BASE_SHA_BG=$(sha256_of "bg/model_probe.go")
echo "基线 SHA: $BASE_SHA (bridge) / $BASE_SHA_CLIENT (client.go) / $BASE_SHA_BG (bg/model_probe.go)"
echo

while IFS=$'\t' read -r id arm_file anchor repl note; do
    [[ -z "$id" ]] && continue

    # 还原两个目标文件（每轮都从基线出发 ⇒ 臂之间不互相污染）
    cp "$BACKUP_DIR/candidate_cache_invalidation.go" "$TARGET_FILE"
    cp "$BACKUP_DIR/client.go" "provider/client.go"
    cp "$BACKUP_DIR/model_probe.go" "bg/model_probe.go"
    base_sha_for_arm=$(sha256_of "$arm_file")

    hits=$(python3 "$HELPER" count "$arm_file" "$(b64 "$anchor")")
    if [[ "$hits" != "1" ]]; then
        echo "❌ $id 锚点命中 $hits 次（应为 1）—— 变异未生效，判据无效"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(锚点命中${hits})"
        continue
    fi

    python3 "$HELPER" replace "$arm_file" "$(b64 "$anchor")" "$(b64 "$repl")" >/dev/null
    rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "❌ $id 替换失败（rc=${rc}）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(替换失败)"
        continue
    fi

    MUT_SHA=$(sha256_of "$arm_file")
    if [[ "$MUT_SHA" == "$base_sha_for_arm" ]]; then
        echo "❌ $id 变异后 SHA 未变 —— 变异根本没生效（不能判为门没牙）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(SHA未变)"
        continue
    fi

    out=$(go test $TEST_PKGS -run "$TEST_PATTERN" -count=1 -timeout 120s 2>&1)
    # ★★ **判定顺序：编译红必须先判** ★★ ——
    #   ★ 编译错的输出**也**以 "FAIL" 开头（"FAIL	<pkg> [build failed]"）⇒
    #   ⇒ 若先判 `^(FAIL|--- FAIL)` ⇒ ⇒ 编译错会被**误判成「门变红」**⇒
    #   ⇒ ⇒ 那就是「红了但定位不准」的同款缺陷（★ LESSONS 同款：
    #   「门红了」的三种成因要分开：断言红 / 编译错 / 门自己坏掉）★★
    #   ⇒ ⇒ 所以**先**查编译错，**再**判断言红。
    if echo "$out" | grep -qE "build failed|cannot use|undefined:|syntax error|too many arguments|not enough arguments"; then
        echo "───── 编译红原始输出（诊断用）─────"
        echo "$out" | grep -nE "build failed|cannot use|undefined:|syntax error|too many arguments|not enough arguments" | head -5
        echo "$out" | head -8
        echo "──────────────────────────────────"
        # ★★★ 编译红的**两种成因必须分开**（★ 2026-11-23 实测撞到）★★★
        #   ① **本臂造成的**：锚点/替换物选错 ⇒ 只有这一臂编译不过 ⇒ ⇒ 换锚点
        #   ② **不是本臂造成的**：★ 被测包的**传递依赖**被**别人**改到一半
        #      ⇒ ⇒ 实测读到过：`vet: domains/streaming/executors/executor_chat.go:
        #         undefined: int64TokenPtr`（★ 并行会话正在改那一行）
        #      ⇒ ⇒ 此时**八条臂会全部编译红**，且报错指向「锚点选错」⇒
        #         ⇒ **归因完全指错方向**，且继续跑下去全是噪音。
        # ⇒ ⇒ 判别法：★ **把臂还原回基线再跑一次同样的命令**
        #   ⇒ 基线也红 ⇒ 成因是 ② ⇒ ⇒ **中止整轮**（继续跑只会产出 8 条假红）
        #   ⇒ 基线绿   ⇒ 成因是 ① ⇒ ⇒ 本臂的锚点/替换物选错
        cp "$BACKUP_DIR/candidate_cache_invalidation.go" "$TARGET_FILE"
        cp "$BACKUP_DIR/client.go" "provider/client.go"
        cp "$BACKUP_DIR/model_probe.go" "bg/model_probe.go"
        if go test $TEST_PKGS -run "$TEST_PATTERN" -count=1 -timeout 120s >/dev/null 2>&1; then
            echo "⚠️  $id 编译红 —— ★ 基线（已还原）**可编译** ⇒ 是**本臂**的锚点/替换物选错（不算判据有牙）"
            FAIL=$((FAIL + 1))
            FAILED_ARMS="${FAILED_ARMS} $id(编译红)"
        else
            echo "❌ $id 编译红 —— ★★ **基线（已还原）同样红** ⇒ 编译红**不是本臂造成的**"
            echo "     ⇒ ⇒ 成因：★ 被测包（${TEST_PKGS[*]}）的**传递依赖**被**别人**改到一半"
            echo "     ⇒ ⇒ ★ 典型实测：「vet: domains/streaming/executors/executor_chat.go: undefined: ...」"
            echo "     ⇒ ⇒ ⇒ 本轮判定已不可信（八条臂会全部编译红、且归因指错方向）⇒ **中止整轮**"
            FAIL=$((FAIL + 1))
            FAILED_ARMS="${FAILED_ARMS} $id(编译红·非本臂)"
            ABORT_SWEEP=1
            break
        fi
    elif echo "$out" | grep -qE "^(FAIL|--- FAIL)"; then
        # 定位精度：红在**哪一条**用例上。
        hit_case=$(echo "$out" | grep -oE "(TestCandidateCacheInvalidation|TestModelProbe)[A-Za-z_]*" | sort -u | tr '\n' ' ')
        echo "✅ $id 门变红（变异生效 SHA=${MUT_SHA:0:8}）—— $note"
        echo "     红在: ${hit_case:-（未定位到用例名）}"
        PASS=$((PASS + 1))
    else
        echo "❌ $id 门仍绿 —— 守卫没牙！"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(门没牙)"
    fi
done <<< "$ARMS"

# ③ 还原后 SHA 复核 + 正向复跑
cp "$BACKUP_DIR/candidate_cache_invalidation.go" "$TARGET_FILE"
cp "$BACKUP_DIR/client.go" "provider/client.go"
cp "$BACKUP_DIR/model_probe.go" "bg/model_probe.go"
RESTORED_SHA=$(sha256_of "$TARGET_FILE")
RESTORED_SHA_CLIENT=$(sha256_of "provider/client.go")
RESTORED_SHA_BG=$(sha256_of "bg/model_probe.go")
echo
if [[ "${RESTORED_SHA}" == "${BASE_SHA}" && "${RESTORED_SHA_CLIENT}" == "${BASE_SHA_CLIENT}" && "${RESTORED_SHA_BG}" == "${BASE_SHA_BG}" ]]; then
    echo "✅ 还原后 SHA 复核一致（bridge=${RESTORED_SHA:0:8} client=${RESTORED_SHA_CLIENT:0:8} bg=${RESTORED_SHA_BG:0:8}）"
else
    echo "❌ 还原后 SHA 不一致：${RESTORED_SHA} != ${BASE_SHA} 或 ${RESTORED_SHA_CLIENT} != ${BASE_SHA_CLIENT} 或 ${RESTORED_SHA_BG} != ${BASE_SHA_BG}"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 还原SHA不一致"
fi

# ★★★ 还原复核**追加对已提交状态**的判定（★ 2026-11-23 加固轮）★★★
# ★ 为什么必须追加这一条：★ 上面那条 SHA 复核比的是**自己的备份** ⇒ ⇒
#   **基线本身脏的时候，它照样打印 ✅** ⇒ ⇒ **自证式复核** ⇒ ⇒
#   门在树被留坏的情况下报「还原成功」（★ 本轮实测读到过）。
# ⇒ ⇒ 追加一条**绝对**判定：还原后工作树必须与 HEAD 逐字一致。
# ⇒ ⇒ 「还原成功」从此是绝对陈述，不再依赖基线是否干净。
if baseline_is_committed; then
    echo "✅ 还原后工作树与已提交状态（HEAD）一致（三个变异目标逐字相同）"
else
    echo "❌ 还原后工作树与 HEAD 不一致 ⇒ **没有还原干净**（★ 上面的 SHA 复核只比备份，是自证式复核）"
    git diff --stat HEAD -- "${MUTATION_TARGETS[@]}"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 还原后与HEAD不一致"
fi

if go test $TEST_PKGS -run "$TEST_PATTERN" -count=1 -timeout 120s >/dev/null 2>&1; then
    echo "✅ 还原后正向复跑通过"
else
    echo "❌ 还原后正向复跑失败"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 还原后复跑失败"
fi

echo
echo "=============================================================="
if [[ "$ABORT_SWEEP" -eq 1 ]]; then
    echo " ❌ 本轮已**中止**：编译红**不是本臂造成的**（★ 基线同样红 ⇒ 并行会话正在改同一棵树）"
    echo "    ⇒ ⇒ 本轮判定**不可信**，请在并行会话的改动落定后复跑。"
    echo "    ⇒ ⇒ ★ 这不是「门没牙」，是「环境在跑的过程中变了」。"
fi
echo " 通过: $PASS   失败: $FAIL"
if [[ "$FAIL" -ne 0 ]]; then
    echo " 失败臂:${FAILED_ARMS}"
    exit 1
fi
echo "=============================================================="
exit 0
