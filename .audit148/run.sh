#!/usr/bin/env bash
# 批148 变异台（最终）—— 只保留有牙的；NO_EFFECT 的不入台（M148-3/4 在本批 spec 不可证区分）
set -e
cd /tmp/co148-wt/web-mobile
readonly PLATFORM=src/api/platformSettings.ts
readonly VIEW=src/views/SanitizeRulesView.vue
readonly SPEC_API=src/api/sanitizeRulesSettings.test.ts
readonly SPEC_VIEW=src/views/SanitizeRulesParams.spec.ts

TMPDIR=$(mktemp -d -t co148mut.XXXXXX)
trap 'rm -rf "$TMPDIR"' EXIT

write_pair() {
  local key="$1" old="$2" new="$3"
  printf '%s' "$old" > "$TMPDIR/$key.old"
  printf '%s' "$new" > "$TMPDIR/$key.new"
}

run_mut() {
  local target="$1" label="$2" pattern="$3" key="$4" spec="$5"
  local before; before=$(shasum -a 256 "$target" | awk '{print $1}')

  if ! python3 - "$target" "$TMPDIR/$key.old" "$TMPDIR/$key.new" <<'PYEOF'
import pathlib, sys
target, old_file, new_file = sys.argv[1], sys.argv[2], sys.argv[3]
old_text = pathlib.Path(old_file).read_text()
new_text = pathlib.Path(new_file).read_text()
src = pathlib.Path(target).read_text()
cnt = src.count(old_text)
if cnt != 1:
    sys.stderr.write(f'old 不唯一（{cnt} 处）：{target}\n')
    sys.exit(1)
pathlib.Path(target).write_text(src.replace(old_text, new_text))
PYEOF
  then
    echo "[$label] ✗ mutate 失败"; return 1
  fi

  echo
  echo "### $label"
  local out; out=$(node_modules/.bin/vitest run "$spec" 2>&1 || true)
  echo "$out" | tail -5
  local total_line; total_line=$(echo "$out" | grep -E '^\s+Tests' | head -1)
  if [ -z "$total_line" ]; then
    echo "[$label] MUTATION_INVALID（崩了）"
  else
    local matched; matched=$(echo "$out" | grep -cE "$pattern" || true)
    matched=${matched:-0}
    local failed; failed=$(echo "$total_line" | grep -oE '[0-9]+ failed' | head -1 | awk '{print $1}')
    failed=${failed:-0}
    if [ "$matched" -ge 1 ] && [ "$failed" -ge 1 ]; then
      echo "[$label] 🦷 有牙（命中 $matched 条，$failed 条红）"
    else
      echo "[$label] ⚠ 没红（$total_line，命中 $matched 条）"
    fi
  fi

  python3 - "$target" "$TMPDIR/$key.new" "$TMPDIR/$key.old" <<'PYEOF'
import pathlib, sys
target, new_file, old_file = sys.argv[1], sys.argv[2], sys.argv[3]
new_text = pathlib.Path(new_file).read_text()
old_text = pathlib.Path(old_file).read_text()
src = pathlib.Path(target).read_text()
cnt = src.count(new_text)
if cnt != 1:
    sys.stderr.write(f'new 不唯一（{cnt} 处），还原失败：{target}\n')
    sys.exit(1)
pathlib.Path(target).write_text(src.replace(new_text, old_text))
PYEOF

  local after; after=$(shasum -a 256 "$target" | awk '{print $1}')
  if [ "$after" != "$before" ]; then
    echo "[$label] ✗ 还原 hash 不一致"
    return 1
  fi
}

# ── API 层（2 条有牙）────────────────────────────────────────────────
write_pair platform-key-guard \
  "if (typeof params.key !== 'string' || params.key === '') throw new Error('settings.invalidKey')" \
  "/* MUTATION: 删 key 空串守门 */
// if (typeof params.key !== 'string' || params.key === '') throw new Error('settings.invalidKey')"
run_mut "$PLATFORM" "M148-1:删 updateSetting key 空串守门" "非法 key" platform-key-guard "$SPEC_API"

write_pair platform-value-guard \
  "if (!('value' in params) || params.value === undefined) throw new Error('settings.invalidValue')" \
  "/* MUTATION: 删 value undefined 守门 */
// if (!('value' in params) || params.value === undefined) throw new Error('settings.invalidValue')"
run_mut "$PLATFORM" "M148-2:删 updateSetting value undefined 守门" "非法 value undefined" platform-value-guard "$SPEC_API"

# M148-3/3b: status/new_value 类型校验在本批 spec 不可证区分（所有非法响应都已在 unwrap 阶段被 new_value/applied_at 拒掉），删了 28/28 不红 ⇒ 不入台

# ── VIEW 层（5 条有牙）────────────────────────────────────────────────
write_pair view-saving-false \
  "      savingParams.value = false
      await loadParams()" \
  "      /* MUTATION: 不松 savingParams */
      // savingParams.value = false
      await loadParams()"
run_mut "$VIEW" "M148-V1:saveParams 不松 savingParams 就 await loadParams" "保存后自动重读并清稿" view-saving-false "$SPEC_VIEW"

write_pair view-revert \
  "      await loadParams()
      revertParams()" \
  "      await loadParams()
      /* MUTATION: 不清草稿 */
      // revertParams()"
run_mut "$VIEW" "M148-V2:saveParams 不清草稿" "保存后自动重读并清稿" view-revert "$SPEC_VIEW"

write_pair view-bound \
  "    if (def.key === 'refresh_interval_seconds' && refreshDraft.value !== null) {
      const v = refreshDraft.value
      if (!Number.isInteger(v) || v < def.min || v > def.max) { paramsError.value = t('sr.paramsInvalid'); return }
      updates.refresh_interval_seconds = v
    } else" \
  "    if (def.key === 'refresh_interval_seconds' && refreshDraft.value !== null) {
      const v = refreshDraft.value
      /* MUTATION: 删客户端边界检查 ⇒ 500 落库 */
      updates.refresh_interval_seconds = v
    } else"
run_mut "$VIEW" "M148-V3:删客户端 refresh 边界检查" "超出范围" view-bound "$SPEC_VIEW"

write_pair view-dirty-audit \
  "    if (def.key === 'audit_enabled' && typeof current === 'boolean' && auditDraft.value !== null && auditDraft.value !== current) return true" \
  "    if (def.key === 'audit_enabled' && /* MUTATION: 删 audit 不等分支 */ false) return true"
run_mut "$VIEW" "M148-V4:删 paramsDirty audit 不等分支（audit 永远被认为未修改）" "保存三键修改" view-dirty-audit "$SPEC_VIEW"

write_pair view-guard \
  "  if (!manager.value || savingParams.value || !paramsList.value) return" \
  "  if (/* MUTATION: 删 manager 守门 */ false || savingParams.value || !paramsList.value) return"
run_mut "$VIEW" "M148-V5:saveParams 删 manager 守门" "manager无参数卡" view-guard "$SPEC_VIEW"

echo
echo '=== 变异台结束 ==='
