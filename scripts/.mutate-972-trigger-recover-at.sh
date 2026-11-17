#!/usr/bin/env bash
# 变异验证：migration 972 —— model_offers_update_trigger 补 unavailable_recover_at
#
# 规则：还原一律用 cp 文件副本，禁止 git checkout --（会静默丢弃未提交工作）
# 规则：python 用引用 heredoc <<'PY'，否则 $'_' 会被 bash 当 ANSI-C 引号展开
# ★★ 本脚本【改树 ⇒ 跑 go test ⇒ 还原】⇒ ⇒ 编译产物的 action ID 就变 ⇒ ⇒ 旧的
# ★× 【永远不会再被命中】⇒ ⇒ 纯垃圾 ⇒ ⇒ 绝不能落在用户的全局缓存里 ⇒ ⇒ 指向
# ★× 【专用缓存】（目录约定与 6 GB 上限见 scripts/lib/gocache.sh）。
#
# 变异清单（每条都必须让门变红）：
#   M1 迁移体里把分支顺序退回「显式给值在前」（= 隔离 PG 真跑腿证伪的那一版）
#   M2 迁移体里整段删掉 unavailable_recover_at 的 CASE（= 修前的形态）
#   M3 删掉 embeddata 副本（= 849/850/851 那一族的漂移）
#   M4 删掉 deploy 通道 files=(...) 里的条目（= 211 通道不会应用）
#   M5 删掉 tsv 台账行（= 账本说没做）
#   M6 删掉 dbinit StartupFiles 条目（= 全新安装不会应用）
#   M7 正典函数快照退回坏分支顺序（= 下一个照抄正典的人会把 bug 抄回去）
#   M8 触发器体里误删 priority 列（= 重写时静默丢列）
set -uo pipefail

export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"

cd "$(dirname "$0")/.."

UP="sql/migrations/startup/972_model_offers_trigger_recover_at.sql"
EMB="installer/cmd/llm-gw-installer/embeddata/startup/972_model_offers_trigger_recover_at.sql"
EMBD="installer/cmd/llm-gw-installer/embeddata/startup/972_model_offers_trigger_recover_at.down.sql"
RUNNER="installer/internal/dbinit/runner.go"
SEQ="scripts/apply-db-revision-sequence.sh"
TSV="sql/schema/installed_startup_migrations.tsv"
SNAP="sql/objects/functions/model_offers_update_trigger.sql"

FILES=("$UP" "$EMB" "$EMBD" "$RUNNER" "$SEQ" "$TSV" "$SNAP" \
  "bg/model_probe.go" "modelcatalog/upsert.go" "admin/credential_monitor.go")
BAK="$(mktemp -d)/mv972"
for f in "${FILES[@]}"; do
  mkdir -p "$BAK/$(dirname "$f")"
  cp "$f" "$BAK/$f"
done
restore() { for f in "${FILES[@]}"; do cp "$BAK/$f" "$f"; done; }
trap restore EXIT

GATE_PKG="./internal/sqlguard/"
# ★ 必须同时跑两道门：
#   ① TestM972…（投递六点位 + 触发器分支顺序）
#   ② TestCredentialBindingHealthTuple…（全仓写入侧四元组不变式）
#   ⇒ 少跑一道 ⇒ 该道的变异会「假绿」（实测：M10~M12 因过滤器漏跑而全绿）。
GATE_RE="TestM972|TestCredentialBindingHealthTupleWritersAreConsistent"

fail=0
mutated=0

# mutate <名称> <目标文件> <python 表达式>
mutate() {
  local name="$1" target="$2" pyexpr="$3"
  echo "----------------- $name -----------------"
  restore
  mutated=$((mutated + 1))
  python3 - "$target" <<PY
import sys
path = sys.argv[1]
src = open(path, encoding='utf-8').read()
$pyexpr
open(path, 'w', encoding='utf-8').write(src)
PY
  if ! go test "$GATE_PKG" -run "$GATE_RE" >/tmp/mv972.log 2>&1; then
    echo "  ✓ 变异被门抓住（红）"
    grep -E '^\s+migration_972|--- FAIL' /tmp/mv972.log | head -4 | sed 's/^/    /'
  else
    echo "  ✗ 变异未被门抓住（**假绿**）—— 门失效"
    fail=$((fail + 1))
  fi
}

# M1 分支顺序退回「显式给值在前」（真跑证伪的那一版）
mutate "M1 迁移体退回坏分支顺序" "$UP" "
src = src.replace('''unavailable_recover_at = CASE
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            ELSE credential_model_bindings.unavailable_recover_at
        END''', '''unavailable_recover_at = CASE
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_recover_at
        END''')
"

# M2 整段删掉该 CASE（= 修前形态）
mutate "M2 迁移体删掉 unavailable_recover_at 的 CASE" "$UP" "
import re
src = re.sub(r'        unavailable_recover_at = CASE\n(?:.*\n)*?        END,\n', '', src)
"

# M3 删掉 embeddata 副本
mutate "M3 删掉 embeddata 副本" "$EMB" "
src = ''
"
mutate "M3b 删掉 embeddata down 副本" "$EMBD" "
src = ''
"

# M4 删掉 deploy 通道 files=(...) 条目
mutate "M4 删掉 deploy 通道条目" "$SEQ" "
src = src.replace('  \"\$ROOT_DIR/sql/migrations/startup/972_model_offers_trigger_recover_at.sql\"\n', '')
"

# M5 删掉 tsv 台账行
mutate "M5 删掉 tsv 台账行" "$TSV" "
src = ''.join(l for l in src.splitlines(True) if '972_model_offers_trigger_recover_at.sql' not in l)
"

# M6 删掉 dbinit StartupFiles 条目
mutate "M6 删掉 dbinit StartupFiles 条目" "$RUNNER" "
src = src.replace('\t\t\t\"972_model_offers_trigger_recover_at.sql\",\n', '')
"

# M7 正典快照退回坏分支顺序
mutate "M7 正典快照退回坏分支顺序" "$SNAP" "
src = src.replace('''unavailable_recover_at = CASE
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            ELSE credential_model_bindings.unavailable_recover_at
        END''', '''unavailable_recover_at = CASE
            WHEN NEW.unavailable_recover_at IS NOT NULL THEN NEW.unavailable_recover_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_recover_at
        END''')
"

# M8 触发器体里误删 priority 列（静默丢列）
mutate "M8 触发器体误删 priority 列" "$UP" "
src = src.replace('        priority = COALESCE(NEW.priority, credential_model_bindings.priority),\n', '')
"

# M10 直写 cmb 的恢复路径退回「不清 recover_at」（= 视图路径之外的漏网路径）
mutate "M10 bg/model_probe.go 退回不清 recover_at" "bg/model_probe.go" "
src = src.replace('''			    unavailable_at     = NULL,
			    -- ★ 972''', '''			    unavailable_at     = NULL,
			    -- ★ 972''')
src = src.replace('''			    unavailable_recover_at = NULL
			FROM provider_models pm''', '''			FROM provider_models pm''')
"

# M11 目录批量刷新退回「不清 recover_at」（实测的主要产生源）
mutate "M11 modelcatalog/upsert.go 退回不清 recover_at" "modelcatalog/upsert.go" "
import re
src = re.sub(r'    unavailable_recover_at = CASE\n(?:.*\n)*?    END\n', '', src, count=1)
"

# M12 手动上线退回「不清 recover_at」
mutate "M12 admin/credential_monitor.go 退回不清 recover_at" "admin/credential_monitor.go" "
src = src.replace('''			    unavailable_recover_at = NULL
			FROM provider_models pm''', '''			FROM provider_models pm''')
"

restore
echo "=========================================="
echo "变异 $mutated 条，未被抓住 $fail 条"
if [[ "$fail" -eq 0 ]]; then
  echo "变异验证：全部变异均被门抓住 ✅"
  echo "=========================================="
  exit 0
fi
echo "变异验证：有变异未被抓住 ❌"
echo "=========================================="
exit 1
