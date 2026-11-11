#!/usr/bin/env bash
# 变异 harness —— 批 5「/v1/responses 入站迁移 IR 主路径」守卫
# （2026-11-11 审计第二轮）
#
# 目标：证明迁移后的分派/消费集/应急回退守卫有牙：
#   M1 responses IR 分派退回 legacy ⇒ 字节级 golden 红（responses wire 裸进
#      ParseOpenAI ⇒ 输出畸变）
#   M2 parse_responses 的 response_format 消费被删 ⇒ 混发参数裸漏 anthropic
#      严格上游 ⇒ golden 400 红
#   M3 anthropic 执行器 responses 分派退回 ParseOpenAI ⇒ golden 红
#   M4 断路器应急回退被禁用 ⇒ 回退钉测红
#   M5 max_completion_tokens 别名消费被删 ⇒ golden 红（规范化丢失）
#
# 用法：bash scripts/audit/ir-round2-responses-migration-mutation.sh

set -uo pipefail

export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
BACKUP_DIR="$(mktemp -d /tmp/ir-round2-resp-mut.XXXXXX)"

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
    # ★ 备份按仓库相对路径镜像存放，必须剥掉 $BACKUP_DIR 前缀。
    while IFS= read -r f; do
        rel="${f#"$BACKUP_DIR"/}"
        cp "$f" "${rel%.bak}"
    done < <(find "$BACKUP_DIR" -name '*.bak' -type f)
}

read -r -d '' ARMS <<'EOF'
M1	domains/streaming/executors/executor_chat.go	if params.ClientProtocol == providercatalog.ProtocolOpenAIResponses && e.IR != nil {	if params.ClientProtocol == providercatalog.ProtocolOpenAIResponses && e.IR != nil && false {	TestResponsesClient_IRMainPath_OpenAIUpstream
M2	internal/ir/parse_responses.go	"response_format": true, "reasoning_effort": true,	"response_format_mutated_away": true, "reasoning_effort": true,	TestUpstreamBodiesUnchanged/responses_to_messages_json
M3	domains/streaming/executors/executor_anthropic.go	if params.ClientProtocol == providercatalog.ProtocolOpenAIResponses {	if params.ClientProtocol == providercatalog.ProtocolOpenAIResponses && false {	TestUpstreamBodiesUnchanged/responses_to_messages_json
M4	domains/streaming/executors/executor_chat.go	&& e.ResponsesToChat != nil {\n\t\t\t\tslog.Warn("ir_converter_circuit_open_fallback_to_legacy_chat",\n\t\t\t\t\t"request_id", params.RequestID,\n\t\t\t\t\t"provider_id", cand.ProviderID,\n\t\t\t\t\t"credential_id", cand.CredentialID,\n\t\t\t\t\t"raw_model", cand.RawModel,\n\t\t\t\t\t"client_model", params.ClientModel,\n\t\t\t\t\t"stage", "parse_responses",	&& false {	TestResponsesClient_CircuitOpen_FallsBackToLegacyChat
M5	internal/ir/parse_responses.go	if src.MaxCompletionTokens != nil && src.MaxOutputTokens == nil {	if src.MaxCompletionTokens != nil && src.MaxOutputTokens == nil && false {	TestUpstreamBodiesUnchanged/responses_to_chat_json
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 批 5 responses 迁移守卫"
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

    # ★ 包映射按**测试名**判定而非目标文件：golden（TestUpstreamBodies*）
    #   住 ./domains/streaming，executor 钉测住 ./domains/streaming/executors
    #   —— 按 target 文件选包会让 -run 匹配不到任何测试 ⇒ 假「门仍绿」。
    case "$expect" in
        TestUpstreamBodies*) PKG=./domains/streaming ;;
        TestResponsesClient*) PKG=./domains/streaming/executors ;;
        *) PKG=./internal/ir ;;
    esac

    out=$(go test "$PKG" -run "$expect" -count=1 2>&1)
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

RESTORED_OUT=$(go test ./domains/streaming ./domains/streaming/executors -run "TestResponsesClient|TestUpstreamBodiesUnchanged/responses_to_chat_json" -count=1 2>&1)
if echo "$RESTORED_OUT" | grep -qE "^ok"; then
    echo " 还原后正向复跑绿 ✅"
    exit 0
else
    echo " 还原后正向复跑仍红 —— 还原不完整！"
    echo "$RESTORED_OUT" | tail -5
    exit 1
fi
