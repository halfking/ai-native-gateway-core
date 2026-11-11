#!/usr/bin/env bash
# 变异 harness —— 批 3「设计模式收口」守卫（2026-11-11 审计第二轮）
#
# 目标：证明三个收口点的守卫有牙：
#   ① responses 桥 toolArgumentsJSON → tooldialect.TerminalArguments（第 4 份
#      内联拷贝清零）—— 改回内联直通 ⇒ 桥钉测红
#   ② tooldialect 终态归一实现（回声剥离/零参回落/直通中立）—— 改坏 ⇒ 方言门红
#   ③ transport pass2 委托 ir.RestoreExtensionsWithDialects 单一语义源 ——
#      改回 no-op ⇒ r1026 pass2 钉测红
#   ④ RestoreExtensionsWithDialects 的 NativePassthrough 门 —— 删除 ⇒ 直通门红
#   ⑤ conversation_shape_fix 分离后的合并默认 —— 改回 warn-only ⇒ 默认翻转门红
#
# 用法：bash scripts/audit/ir-round2-pattern-mutation.sh

set -uo pipefail

export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
BACKUP_DIR="$(mktemp -d /tmp/ir-round2-pattern-mut.XXXXXX)"

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
M1	domains/streaming/responses_bridge.go	return tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages).TerminalArguments(arguments)	return arguments	TestResponsesBridgePlaceholderEchoTerminalArgsNormalized
M2	internal/tooldialect/tooldialect.go	acc := stripPlaceholderEcho("", accumulated)	acc := accumulated	TestAnthropicTerminalArguments
M3	internal/tooldialect/tooldialect.go	if start == "" && strings.HasPrefix(buffered, "{}") && len(buffered) > 2 {	if false {	TestAnthropicTerminalArguments
M4	domains/transformation/ir_converter.go	ir.RestoreExtensionsWithDialects(anyOut, req, src, dst, targetProtocol)	_ = req\n\t_ = src\n\t_ = dst\n\t_ = targetProtocol	TestR1026Pass2StreamOptionsKeptOnChatWire
M5	internal/ir/extensions_restore.go	if req.NativePassthrough {	if req.NativePassthrough && false {	TestRestoreExtensions_NativePassthrough_KeepsDialectPrivateFields
M6	internal/ir/conversation_shape_fix.go	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") != "false"	shouldFix := os.Getenv("LLM_GATEWAY_FIX_ROLE_ALTERNATION") == "true"	TestClientShape_RoleAlternationDefaultMerged
M7	internal/tooldialect/tooldialect.go	func (passthrough) TerminalArguments(accumulated string) string { return accumulated }	func (passthrough) TerminalArguments(accumulated string) string { return "{}" }	TestRegistryOpenForExtension
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 批 3 设计模式收口守卫"
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

    case "$target" in
        domains/streaming/*) PKG=./domains/streaming ;;
        domains/transformation/*) PKG=./domains/transformation ;;
        *) PKG=./internal/ir ;;
    esac
    case "$expect" in
        TestAnthropicTerminalArguments|TestRegistryOpenForExtension) PKG=./internal/tooldialect ;;
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

RESTORED_OUT=$(go test ./internal/tooldialect ./internal/ir ./domains/transformation -run "TestAnthropicTerminalArguments|TestRegistryOpenForExtension|TestRestoreExtensions_NativePassthrough|TestClientShape_RoleAlternationDefaultMerged|TestR1026Pass2" -count=1 2>&1)
if echo "$RESTORED_OUT" | grep -qE "^(ok|--- PASS)" && ! echo "$RESTORED_OUT" | grep -qE "FAIL"; then
    echo " 还原后正向复跑绿 ✅"
    exit 0
else
    echo " 还原后正向复跑仍红 —— 还原不完整！"
    echo "$RESTORED_OUT" | tail -5
    exit 1
fi
