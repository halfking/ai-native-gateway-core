#!/usr/bin/env bash
# 变异 harness —— 客户端错误修正守卫（2026-11-11 审计轮）
#
# 目的：证明 internal/ir/client_error_fix_test.go 里的守卫**真的有牙**，
#       而不是「断言写对了但永远绿」。
#
# 三件套纪律（缺任何一件 ⇒ 结论不成立）：
#   ① 变异**生效**的证据：目标文件 SHA 变化（独立于门）
#   ② 门**变红**：目标测试用例 FAIL
#   ③ 还原后 **SHA 复核 + 正向复跑**
#
# 备份时机：备份取在**补丁已上之后**（本轮补丁 = parse_anthropic.go 的
# OpenAI 工具归一化 + tool_extra.go 的 toolKnownKeysWith）。
# 取在补丁前会把补丁本身一起冲掉 ⇒ 后续步骤在错误前提上跑。
#
# 用法：bash scripts/audit/ir-client-error-fix-mutation.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
TARGET_FILE="internal/ir/parse_anthropic.go"
BACKUP_DIR="$(mktemp -d /tmp/ir-client-fix-mut.XXXXXX)"
TEST_PATTERN="TestClientError_"

cleanup() {
    if [[ -f "$BACKUP_DIR/parse_anthropic.go" ]]; then
        cp "$BACKUP_DIR/parse_anthropic.go" "$TARGET_FILE"
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

# ── 变异臂定义（tab 分隔：id / 锚点 / 替换物 / 期望失败的用例）────────────
# 锚点必须**唯一命中 1 次**（helper 会拒绝并返回命中数）——
# 多行锚点用 \n / \t 转义表达（helper 的 unescape 还原）。
#
# M4/M5 的锚点必须带 `fn["..."]` 上下文：flat 分支里也有 `td.Name = name`
# / `td.Description = desc`，只锚赋值语句会命中 2 次。
read -r -d '' ARMS <<'EOF'
M1	isBuiltin := toolType != "" && toolType != "custom_tool" && toolType != "function"	isBuiltin := toolType != "" && toolType != "custom_tool"	TestClientError_OpenAIToolFormatToAnthropic
M2	(toolType == "function" || toolType == "")	(toolType == "__never_match__")	TestClientError_OpenAIToolFormatToAnthropic
M3	toolKnownKeysWith(irToolKnownKeysAnthropic, "function")	irToolKnownKeysAnthropic	TestClientError_OpenAIToolFormatToAnthropic
M4	if desc, ok := fn["description"].(string); ok {\n\t\t\t\ttd.Description = desc\n\t\t\t}	if desc, ok := fn["description"].(string); ok {\n\t\t\t\ttd.Description = ""\n\t\t\t}	TestClientError_OpenAIToolFormatToAnthropic
M5	if name, ok := fn["name"].(string); ok {\n\t\t\t\ttd.Name = name\n\t\t\t}	if name, ok := fn["name"].(string); ok {\n\t\t\t\ttd.Name = ""\n\t\t\t}	TestClientError_OpenAIToolFormatToAnthropic
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 客户端错误修正守卫"
echo " 目标文件: $TARGET_FILE"
echo "=============================================================="

cp "$TARGET_FILE" "$BACKUP_DIR/parse_anthropic.go"
BASE_SHA=$(sha256_of "$TARGET_FILE")
echo "基线 SHA: $BASE_SHA"
echo

while IFS=$'\t' read -r id anchor repl expect; do
    [[ -z "$id" ]] && continue

    # 还原到基线
    cp "$BACKUP_DIR/parse_anthropic.go" "$TARGET_FILE"

    # ① 变异生效的证据：锚点必须恰好命中 1 次
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

    # ① 变异生效的独立证据：SHA 必须变化
    MUT_SHA=$(sha256_of "$TARGET_FILE")
    if [[ "$MUT_SHA" == "$BASE_SHA" ]]; then
        echo "❌ $id 变异后 SHA 未变 —— 变异根本没生效（不能判为门没牙）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(SHA未变)"
        continue
    fi

    # ② 门必须变红
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
cp "$BACKUP_DIR/parse_anthropic.go" "$TARGET_FILE"
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
    echo " 未通过:$FAILED_ARMS"
fi
echo "=============================================================="

[[ $FAIL -eq 0 ]] || exit 1
