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
}
trap cleanup EXIT

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
        echo "❌ $id 替换失败（rc=$rc）"
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
        echo "⚠️  $id 编译红 —— 不算判据有牙（锚点/替换物选错）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(编译红)"
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

if go test $TEST_PKGS -run "$TEST_PATTERN" -count=1 -timeout 120s >/dev/null 2>&1; then
    echo "✅ 还原后正向复跑通过"
else
    echo "❌ 还原后正向复跑失败"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 还原后复跑失败"
fi

echo
echo "=============================================================="
echo " 通过: $PASS   失败: $FAIL"
if [[ "$FAIL" -ne 0 ]]; then
    echo " 失败臂:${FAILED_ARMS}"
    exit 1
fi
echo "=============================================================="
exit 0
