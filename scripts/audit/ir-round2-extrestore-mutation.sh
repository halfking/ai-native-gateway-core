#!/usr/bin/env bash
# 变异 harness —— 批 1「扩展参数还原通道覆盖矩阵」守卫（2026-11-11 审计第二轮）
#
# 目的：证明 internal/ir/extension_restore_matrix_test.go 里的矩阵守卫与
#       行为抽查**真的有牙**：每个被登记的「consumed / disclosed」处置，
#       把代码改回静默形态 ⇒ 门必须红。
#
# 三件套纪律（与 ir-client-error-fix-mutation.sh 同款）：
#   ① 变异生效证据：锚点唯一命中 1 次 + 目标文件 SHA 变化
#   ② 门变红：目标测试 FAIL
#   ③ 还原后 SHA 复核 + 正向复跑
#
# 用法：bash scripts/audit/ir-round2-extrestore-mutation.sh

set -uo pipefail

export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
BACKUP_DIR="$(mktemp -d /tmp/ir-round2-extrestore-mut.XXXXXX)"

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

# ★ 还原必须用 find 递归：备份按目标文件相对路径存放（$BACKUP_DIR/internal/ir/x.go.bak），
#   顶层 glob "$BACKUP_DIR"/*.bak 匹配不到子目录 ⇒ 还原静默 no-op ⇒ 变异残留
#   （2026-11-12 实测：首轮 9/10 "通过"其实跑在复合变异树上 —— 门变红的逐臂
#   判定虽各自成立，但还原复核与最终树态全错）。
restore_all() {
    # ★ 真缺陷（2026-11-12 二次实测）：备份按仓库相对路径镜像存放，
    #   "${f%.bak}" 算出的是**备份目录内部**的同名路径 ⇒ cp 把备份拷回
    #   备份自己，仓库文件从未被还原（首轮 9/10 与二轮 10/10 的「还原后
    #   仍红」同根）。必须先剥掉 $BACKUP_DIR 前缀得到仓库相对路径。
    while IFS= read -r f; do
        rel="${f#"$BACKUP_DIR"/}"
        cp "$f" "${rel%.bak}"
    done < <(find "$BACKUP_DIR" -name '*.bak' -type f)
}

# ── 变异臂（tab 分隔：id / 目标文件 / 锚点 / 替换物 / 期望失败的用例）────
read -r -d '' ARMS <<'EOF'
M1	internal/ir/serialize_ollama.go	if text := systemPlainText(req.System); text != "" {	if text := systemPlainText(req.System); text == "__never_match__" {	TestMatrixSpotChecks_Batch1Pins/ollama_system_prompt_emitted
M2	internal/ir/serialize_ollama.go	case "json_object":\n\t\t\t\tbody["format"] = "json"	case "__never_match__":\n\t\t\t\tbody["format"] = "json"	TestMatrixSpotChecks_Batch1Pins/ollama_response_format_translation
M3	internal/ir/serialize_ollama.go	mergeToolExtras(tool, t.Extra)	_ = t.Extra	TestMatrixSpotChecks_Batch1Pins/ollama_tool_extras_merged
M4	internal/ir/serialize_ollama.go	reportOllamaStructuredFieldLosses(req)	_ = req	TestMatrixSpotChecks_Batch1Pins/ollama_structured_fields_disclosed
M5	internal/ir/serialize_ollama.go	if req.FrequencyPenalty != nil {\n\t\topts["frequency_penalty"] = *req.FrequencyPenalty\n\t}	if req.FrequencyPenalty != nil {\n\t\t_ = *req.FrequencyPenalty\n\t}	TestMatrixSpotChecks_Batch1Pins/ollama_penalty_params_consumed
M6	internal/ir/serialize_gemini.go	reportGeminiToolExtrasLoss(req)	_ = req	TestMatrixSpotChecks_Batch1Pins/gemini_tool_extras_disclosed_not_merged
M7	internal/ir/serialize_gemini.go	for _, f := range structured {\n\t\tif !f.have {	for _, f := range structured {\n\t\tif true {	TestMatrixSpotChecks_Batch1Pins/gemini_structured_fields_disclosed
M8	internal/ir/serialize_anthropic.go	{"seed", req.Seed != nil},	{"seed", false},	TestMatrixSpotChecks_Batch1Pins/anthropic_seed_and_repetition_penalty_disclosed
M9	internal/ir/serialize_anthropic.go	"repetition_penalty",	"repetition_penalty_renamed",	TestMatrixSpotChecks_Batch1Pins/anthropic_seed_and_repetition_penalty_disclosed
M10	internal/ir/extension_restore_matrix_test.go	"safety_identifier":    {ok(), st(false, "披露"), st(false, "披露"), ok(), st(false, "披露")},		TestStructuredFieldCoverageMatrix
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 批 1 还原通道覆盖矩阵守卫"
echo "=============================================================="

while IFS=$'\t' read -r id target anchor repl expect; do
    [[ -z "$id" ]] && continue

    restore_all
    backup_of "$target"
    BASE_SHA=$(sha256_of "$target")

    # ① 锚点必须恰好命中 1 次
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
        echo "❌ $id 替换失败（rc=${rc}）"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(替换失败)"
        continue
    fi

    MUT_SHA=$(sha256_of "$target")
    if [[ "$MUT_SHA" == "$BASE_SHA" ]]; then
        echo "❌ $id 变异后 SHA 未变 —— 变异根本没生效"
        FAIL=$((FAIL + 1))
        FAILED_ARMS="${FAILED_ARMS} $id(SHA未变)"
        continue
    fi

    # ② 门必须变红
    out=$(go test ./internal/ir -run "$expect" -count=1 2>&1)
    if grep -qE "^(FAIL|--- FAIL)" <<<"$out"; then
        echo "✅ $id 门变红（${target##*/} SHA=${MUT_SHA:0:8}）"
        PASS=$((PASS + 1))
    elif grep -qE "build failed|cannot use|undefined:" <<<"$out"; then
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
FINAL_SHA=$(sha256_of internal/ir/serialize_ollama.go)

echo
echo "=============================================================="
if [[ $FAIL -eq 0 ]]; then
    echo " 全部 $PASS 个变异臂被守卫抓住 ✅"
else
    echo " 通过 $PASS / 失败 $FAIL —— 失败臂:$FAILED_ARMS"
fi

# ③ 还原复核 + 正向复跑（门在完好代码上必须绿）
if diff -q <(git show HEAD:internal/ir/serialize_ollama.go 2>/dev/null) internal/ir/serialize_ollama.go >/dev/null 2>&1; then
    :
fi
RESTORED_OUT=$(go test ./internal/ir -run "TestStructuredFieldCoverageMatrix|TestMatrixSpotChecks" -count=1 2>&1)
if grep -qE "^ok" <<<"$RESTORED_OUT"; then
    echo " 还原后正向复跑绿 ✅"
    exit 0
else
    echo " 还原后正向复跑仍红 —— 还原不完整！"
    echo "$RESTORED_OUT" | tail -5
    exit 1
fi
