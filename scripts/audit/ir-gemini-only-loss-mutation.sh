#!/usr/bin/env bash
# 变异 harness —— Gemini 专有参数丢失披露守卫（2026-11-11 审计轮）
#
# 目的：证明 internal/ir/gemini_only_loss_test.go 里的守卫**真的有牙**。
# 重点防两类退化：
#   ① 「上报被摘掉」—— 有 loss 事件这一层没了（回到静默丢弃）。
#   ② 「上报被打瞎」—— 事件发了但字段名/协议对不上（形似而神不似）。
#
# 三件套纪律：
#   ① 变异**生效**的证据：目标文件 SHA 变化（独立于门）
#   ② 门**变红**：目标测试用例 FAIL
#   ③ 还原后 **SHA 复核 + 正向复跑**
#
# 备份时机：补丁已上之后（避免还原时把补丁一起冲掉）。
#
# 用法：bash scripts/audit/ir-gemini-only-loss-mutation.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
TARGET_FILE="internal/ir/gemini_only_loss.go"
BACKUP_DIR="$(mktemp -d /tmp/ir-gem-loss-mut.XXXXXX)"
TEST_PATTERN="TestGeminiOnlyLoss_"

cleanup() {
    if [[ -f "$BACKUP_DIR/gemini_only_loss.go" ]]; then
        cp "$BACKUP_DIR/gemini_only_loss.go" "$TARGET_FILE"
    fi
    rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

# ★ 可移植 SHA-256（仓内既有惯例，见 scripts/retire-request-logs-v1-family.sh:148
#   与 scripts/verify-migration-checksums.sh:58）：
#   ★ `shasum` 是 macOS/Perl 系的命令 ⇒ **Linux 容器（尤其精简镜像）未必有**
#   ⇒ ★ 只用 `shasum` 会让本 harness 在 CI 载体上「命令不存在」⇒ 假红。
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# 变异臂（tab 分隔：id / 锚点 / 替换物 / 期望失败的用例）
read -r -d '' ARMS <<'EOF'
G1	if len(req.SafetySettings) > 0 {	if false {
G2	if req.CachedContent != "" {	if false {
G3	"safety_settings"	"__renamed_field__"
G4	"safety_settings",\n\t\t\tProtocolGeminiGenerate,	"safety_settings",\n\t\t\tProtocolOpenAIChat,
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— Gemini 专有参数丢失披露守卫"
echo " 目标文件: $TARGET_FILE"
echo "=============================================================="

cp "$TARGET_FILE" "$BACKUP_DIR/gemini_only_loss.go"
BASE_SHA=$(sha256_of "$TARGET_FILE")
echo "基线 SHA: $BASE_SHA"
echo

while IFS=$'\t' read -r id anchor repl expect; do
    [[ -z "$id" ]] && continue

    cp "$BACKUP_DIR/gemini_only_loss.go" "$TARGET_FILE"

    hits=$(python3 "$HELPER" count "$TARGET_FILE" "$(b64 "$anchor")")
    if [[ "$hits" != "1" ]]; then
        echo "❌ $id 锚点命中 $hits 次（应为 1）—— 变异未生效，判据无效"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(锚点命中${hits})"
        continue
    fi

    python3 "$HELPER" replace "$TARGET_FILE" "$(b64 "$anchor")" "$(b64 "$repl")" >/dev/null
    rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "❌ $id 替换失败（rc=$rc）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(替换失败)"
        continue
    fi

    MUT_SHA=$(sha256_of "$TARGET_FILE")
    if [[ "$MUT_SHA" == "$BASE_SHA" ]]; then
        echo "❌ $id 变异后 SHA 未变 —— 变异根本没生效（不能判为门没牙）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(SHA未变)"
        continue
    fi

    out=$(go test ./internal/ir -run "$expect" -count=1 2>&1)
    if echo "$out" | grep -qE "^(FAIL|--- FAIL)"; then
        echo "✅ $id 门变红（变异生效 SHA=${MUT_SHA:0:8}）"
        PASS=$((PASS + 1))
    else
        if echo "$out" | grep -qE "build failed|cannot use|undefined:"; then
            echo "⚠️  $id 编译红 —— 不算判据有牙（锚点选错）"
            FAIL=$((FAIL + 1))
            FAILED_ARMS="${FAILED_ARMS} $id(编译红)"
        else
            echo "❌ $id 门仍绿 —— 守卫没牙！"
            FAIL=$((FAIL + 1))
            FAILED_ARMS="${FAILED_ARMS} $id(门没牙)"
        fi
    fi
done <<< "$ARMS"

# ③ 还原后 SHA 复核 + 正向复跑
cp "$BACKUP_DIR/gemini_only_loss.go" "$TARGET_FILE"
RESTORED_SHA=$(sha256_of "$TARGET_FILE")
echo
if [[ "${RESTORED_SHA}" == "${BASE_SHA}" ]]; then
    echo "✅ 还原后 SHA 复核一致（${RESTORED_SHA}）"
else
    echo "❌ 还原后 SHA 不一致：${RESTORED_SHA} != ${BASE_SHA}"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 还原SHA不一致"
fi

if go test ./internal/ir -run "$TEST_PATTERN" -count=1 >/dev/null 2>&1; then
    echo "✅ 还原后正向复跑通过"
else
    echo "❌ 还原后正向复跑失败"
    FAIL=$((FAIL + 1))
    FAILED_ARMS="${FAILED_ARMS} 正向复跑失败"
fi

echo
echo "=============================================================="
echo " 结果: 抓住 $PASS / $((PASS + FAIL))"
if [[ -n "$FAILED_ARMS" ]]; then
    echo " 未通过:${FAILED_ARMS}"
fi
echo "=============================================================="

[[ $FAIL -eq 0 ]] || exit 1
