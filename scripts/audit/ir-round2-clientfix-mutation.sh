#!/usr/bin/env bash
# 变异 harness —— 批 2「客户端错误格式修正」守卫（2026-11-11 审计第二轮）
#
# 目的：证明 internal/ir/client_shape_fix_test.go 的守卫有牙：把每个修正/
#       披露改回静默形态 ⇒ 门必须红。
# 复用 ir-round2-extrestore-mutation.sh 的三件套纪律与 find 路径换算还原
#（2026-11-12 修复的 "${f%.bak}" 备份自拷贝缺陷）。
#
# 用法：bash scripts/audit/ir-round2-clientfix-mutation.sh

set -uo pipefail

export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
BACKUP_DIR="$(mktemp -d /tmp/ir-round2-clientfix-mut.XXXXXX)"

cleanup() {
    restore_all
    rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

backup_of() {
    local f="$1"
    [[ -f "$BACKUP_DIR/$f.bak" ]] || { mkdir -p "$BACKUP_DIR/$(dirname "$f")"; cp "$f" "$BACKUP_DIR/$f.bak"; }
}

restore_all() {
    # ★ 备份按仓库相对路径镜像存放，必须剥掉 $BACKUP_DIR 前缀
    #   （extrestore harness 的 "${f%.bak}" 备份自拷贝缺陷同款教训）。
    while IFS= read -r f; do
        rel="${f#"$BACKUP_DIR"/}"
        cp "$f" "${rel%.bak}"
    done < <(find "$BACKUP_DIR" -name '*.bak' -type f)
}

read -r -d '' ARMS <<'EOF'
M1	internal/ir/client_shape_fix.go	if id, ok := msg["tool_call_id"].(string); ok && id != "" {	if id, ok := msg["tool_call_id"].(string); ok && id != "" && id == "__never__" {	TestClientShape_RoleNormalization
M2	internal/ir/client_shape_fix.go	reportClientShapeFix(sourceProtocol, "role", "inferred_user",	reportClientShapeFix(sourceProtocol, "role", "inferred_user_renamed",	TestClientShape_RoleNormalization
M3	internal/ir/client_shape_fix.go		if args == "" {		if args == "" && false {	TestClientShape_ToolCallArguments
M4	internal/ir/client_shape_fix.go	if !json.Valid([]byte(args)) {	if false {	TestClientShape_ToolCallArguments
M5	internal/ir/client_shape_fix.go	reportClientShapeFix(sourceProtocol, what, "non_object_element_skipped",	reportClientShapeFix(sourceProtocol, what, "non_object_element_renamed",	TestClientShape_NonObjectElementDisclosed
M6	internal/ir/parse_anthropic.go	if inferred, ok := inferRoleFromContentBlocks(irMsg.Content); ok {	if inferred, ok := inferRoleFromContentBlocks(irMsg.Content); ok && false {	TestClientShape_RoleNormalization
M7	internal/ir/validate_and_fix.go	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") != "false"	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") == "__never__"	TestClientShape_RoleAlternationDefaultMerged
M8	internal/ir/parse_openai.go	reportClientShapeFix(ProtocolOpenAIChat, "modalities", "unmarshal_failed_dropped",	reportClientShapeFix(ProtocolOpenAIChat, "modalities", "unmarshal_failed_renamed",	TestClientShape_CompoundFieldMalformed
M9	internal/ir/parse_openai.go	reportClientShapeFix(ProtocolOpenAIChat, "stop", "unparseable_dropped",	reportClientShapeFix(ProtocolOpenAIChat, "stop", "unparseable_renamed",	TestClientShape_CompoundFieldMalformed
M10	internal/ir/parse_gemini.go	reportClientShapeFix(ProtocolGeminiGenerate, "contents[].parts[].inlineData", "malformed_part_dropped",	reportClientShapeFix(ProtocolGeminiGenerate, "contents[].parts[].inlineData", "malformed_part_renamed",	TestClientShape_GeminiMalformedParts
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 批 2 客户端错误修正守卫"
echo "=============================================================="

while IFS=$'\t' read -r id target anchor repl expect; do
    [[ -z "$id" ]] && continue

    restore_all
    backup_of "$target"
    BASE_SHA=$(sha256_of "$target")

    hits=$(python3 "$HELPER" count "$target" "$(b64 "$anchor")")
    if [[ "$hits" != "1" ]]; then
        echo "❌ $id 锚点命中 $hits 次（应为 1）—— 变异未生效，判据无效"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(锚点命中${hits})"
        continue
    fi

    python3 "$HELPER" replace "$target" "$(b64 "$anchor")" "$(b64 "$repl")" >/dev/null
    rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "❌ $id 替换失败（rc=$rc）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(替换失败)"
        continue
    fi

    MUT_SHA=$(sha256_of "$target")
    if [[ "$MUT_SHA" == "$BASE_SHA" ]]; then
        echo "❌ $id 变异后 SHA 未变"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(SHA未变)"
        continue
    fi

    out=$(go test ./internal/ir -run "$expect" -count=1 2>&1)
    if echo "$out" | grep -qE "^(FAIL|--- FAIL)"; then
        echo "✅ $id 门变红（${target##*/} SHA=${MUT_SHA:0:8}）"
        PASS=$((PASS + 1))
    elif echo "$out" | grep -qE "build failed|cannot use|undefined:|vet:"; then
        echo "⚠️  $id 编译红 —— 不算判据有牙（锚点选错）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(编译红)"
    else
        echo "❌ $id 门仍绿 —— 守卫没牙！"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(门没牙)"
    fi
done <<< "$ARMS"

restore_all

echo
echo "=============================================================="
if [[ $FAIL -eq 0 ]]; then
    echo " 全部 $PASS 个变异臂被守卫抓住 ✅"
else
    echo " 通过 $PASS / 失败 $FAIL —— 失败臂:$FAILED_ARMS"
fi

RESTORED_OUT=$(go test ./internal/ir -run "TestClientShape" -count=1 2>&1)
if echo "$RESTORED_OUT" | grep -qE "^ok"; then
    echo " 还原后正向复跑绿 ✅"
    exit 0
else
    echo " 还原后正向复跑仍红 —— 还原不完整！"
    echo "$RESTORED_OUT" | tail -5
    exit 1
fi
