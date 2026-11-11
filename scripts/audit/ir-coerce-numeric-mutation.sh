#!/usr/bin/env bash
# 变异 harness —— 客户端数值/布尔参数容错层守卫（2026-11-11 审计轮）
#
# 目的：证明 internal/ir/coerce_numeric_test.go 里的守卫**真的有牙**。
# 重点防三类退化：
#   ① 「不再转」—— 容错层整体失效（回到硬失败）
#   ② 「转太宽」—— 无歧义性判据被放松（"abc" 也被转 ⇒ 变成瞎猜）
#   ③ 「转太宽 2」—— 字符串类型字段被误转（model="12345" ⇒ 12345）
#
# 三件套纪律：
#   ① 变异**生效**的证据：目标文件 SHA 变化（独立于门）
#   ② 门**变红**：目标测试用例 FAIL
#   ③ 还原后 **SHA 复核 + 正向复跑**
#
# 备份时机：补丁已上之后（避免还原时把补丁一起冲掉）。
#
# 用法：bash scripts/audit/ir-coerce-numeric-mutation.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

HELPER="scripts/audit/_mutate_helper.py"
TARGET_FILE="internal/ir/coerce_numeric.go"
BACKUP_DIR="$(mktemp -d /tmp/ir-coerce-mut.XXXXXX)"
TEST_PATTERN="TestCoerceNumeric_"

cleanup() {
    if [[ -f "$BACKUP_DIR/coerce_numeric.go" ]]; then
        cp "$BACKUP_DIR/coerce_numeric.go" "$TARGET_FILE"
    fi
    rm -rf "$BACKUP_DIR"
}
trap cleanup EXIT

b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

# 变异臂（tab 分隔：id / 锚点 / 替换物 / 期望失败的用例）
read -r -d '' ARMS <<'EOF'
C1	for ft.Kind() == reflect.Pointer {	if false {	TestCoerceNumeric_
C2	f, err := strconv.ParseFloat(s, 64)\n\t\tif err != nil {\n\t\t\treturn "", false\n\t\t}	f, err := strconv.ParseFloat(s, 64)\n\t\tif err != nil {\n\t\t\treturn "0", true\n\t\t}	TestCoerceNumeric_
C3		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,\n\t\t\treflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,\n\t\t\treflect.Float32, reflect.Float64:		case reflect.Int, reflect.Int64:	TestCoerceNumeric_
C4		case reflect.Bool:\n\t\t\tout[name] = reflect.Bool\n\t\tdefault:		case reflect.Bool:\n\t\t\tout[name] = reflect.Bool\n\t\tcase reflect.String:\n\t\t\tout[name] = reflect.Float64\n\t\tdefault:	TestCoerceNumeric_
EOF

PASS=0
FAIL=0
FAILED_ARMS=""

echo "=============================================================="
echo " 变异 harness —— 数值/布尔容错层守卫"
echo " 目标文件: $TARGET_FILE"
echo "=============================================================="

cp "$TARGET_FILE" "$BACKUP_DIR/coerce_numeric.go"
BASE_SHA=$(shasum -a 256 "$TARGET_FILE" | awk '{print $1}')
echo "基线 SHA: $BASE_SHA"
echo

while IFS=$'\t' read -r id anchor repl expect; do
    [[ -z "$id" ]] && continue

    cp "$BACKUP_DIR/coerce_numeric.go" "$TARGET_FILE"

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

    MUT_SHA=$(shasum -a 256 "$TARGET_FILE" | awk '{print $1}')
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
cp "$BACKUP_DIR/coerce_numeric.go" "$TARGET_FILE"
RESTORED_SHA=$(shasum -a 256 "$TARGET_FILE" | awk '{print $1}')
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
