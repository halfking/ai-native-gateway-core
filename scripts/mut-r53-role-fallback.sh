#!/bin/bash
# R53 mutation harness — 证明门承重，不是恒绿。
#
# ⚠️ 本脚本会**临时改写源码**（metrics.go / decision.go / 告警 yml）。
#    运行期间不要编辑这几个文件，也不要并发跑 go test：还原动作会把你的
#    并行编辑整体抹掉，而后续测试会红在一个你没改过的地方。
#
# 用途：改动 role 兜底告警相关代码后重跑，确认门仍然承重。
# 一次性验证脚本，不是生产工具。
#
# 纪律（来自既往踩坑）：
#  - 备份登记**无条件**做（BACKUPS+= 在 if 之外）：同一文件被变异两次时
#    靠 "备份已存在" 判重会让第二次变更永不登记，restore 静默跳过，
#    后续测试红在一个没被本次改动影响的地方。
#  - 每次跑前先跑 control（未变异必须全绿），否则无法区分"门红"和"变异残留"。
#  - 跑完核对无残留 .mutbak。

set -uo pipefail
cd "$(dirname "$0")/.."

FILES=(
  "autoroute/metrics.go"
  "autoroute/decision.go"
  "deploy/prometheus/rules/role-fallback-mainstream.yml"
)
BACKUPS=()
BAKDIR="$(mktemp -d)"

snapshot() {
  for f in "${FILES[@]}"; do
    bak="$BAKDIR/$(echo "$f" | tr '/' '_')"
    cp "$f" "$bak"
    BACKUPS+=("$f|$bak")   # 无条件登记
  done
}

restore() {
  for pair in "${BACKUPS[@]}"; do
    f="${pair%%|*}"; bak="${pair##*|}"
    if [ ! -f "$bak" ]; then
      echo "!! 备份缺失，无法还原 $f" >&2
      continue
    fi
    cp "$bak" "$f"
  done
  BACKUPS=()
}

run_gate() {
  local name="$1" dir="$2" pattern="$3"
  ( cd "$dir" && go test ./ -count=1 -run "$pattern" 2>&1 | tail -6 )
}

echo "=========== CONTROL（未变异，必须全绿）==========="
( cd autoroute && go test ./ -count=1 -run 'TestRoleFallback|TestRecordRoleFallbackLayer|TestRoleLayer' 2>&1 | tail -3 )
( cd deploy/prometheus/rules && go test ./ -count=1 -run 'TestRoleFallback' 2>&1 | tail -3 )

# ---- M1: 删掉 Add(0) 预置 --------------------------------------------
echo; echo "=========== M1: 删除 layer series 的 Add(0) 预置 ==========="
snapshot
python3 - <<'PY'
import re,io
p='autoroute/metrics.go'
s=open(p,encoding='utf-8').read()
before=s
s=s.replace('\tlayers.WithLabelValues(RoleLayerKind).Add(0)\n\tlayers.WithLabelValues(RoleLayerMainstream).Add(0)\n','')
assert s!=before, "M1 未命中：预置行文本与预期不符"
open(p,'w',encoding='utf-8').write(s)
print("M1 applied")
PY
run_gate M1 autoroute 'TestRoleFallbackMetricsExposeBothLayersWhenFeatureDisabled'
restore

# ---- M2: 删掉告警里的 role_routing_active 门 ---------------------------
echo; echo "=========== M2: 把 and on() 退化成裸 and（标签不匹配 → 空向量） ==========="
snapshot
python3 - <<'PY'
p='deploy/prometheus/rules/role-fallback-mainstream.yml'
s=open(p,encoding='utf-8').read()
before=s
s=s.replace('          and on()\n          max(llmgw_autoroute_role_routing_active) == 1\n','          and max(llmgw_autoroute_role_routing_active) == 1\n',1)
assert s!=before, "M2 未命中：gate 行文本与预期不符"
open(p,'w',encoding='utf-8').write(s)
print("M2 applied")
PY
run_gate M2 deploy/prometheus/rules 'TestRoleFallbackMainstreamSpikeRuleUsesRegisteredMetric'
restore

# ---- M3: 删掉样本量下界（同一 YAML 文件的第二次变异）-------------------
echo; echo "=========== M3: 删掉 YAML 的样本量下界 >= 5 ==========="
snapshot
python3 - <<'PY'
p='deploy/prometheus/rules/role-fallback-mainstream.yml'
s=open(p,encoding='utf-8').read()
before=s
s=s.replace('          and on()\n          sum(increase(llmgw_autoroute_role_fallback_layer_total[15m])) >= 5\n','',1)
assert s!=before, "M3 未命中：样本下界行文本与预期不符"
open(p,'w',encoding='utf-8').write(s)
print("M3 applied")
PY
run_gate M3 deploy/prometheus/rules 'TestRoleFallbackMainstreamSpikeRuleUsesRegisteredMetric'
restore

# ---- M4: 让 layerName 与常量分叉 ---------------------------------------
echo; echo "=========== M4: layerName(1) 返回 'main'（与常量分叉）==========="
snapshot
python3 - <<'PY'
p='autoroute/decision.go'
s=open(p,encoding='utf-8').read()
before=s
s=s.replace('\tcase 1:\n\t\treturn RoleLayerKind\n','\tcase 1:\n\t\treturn "main"\n',1)
assert s!=before, "M4 未命中：layerName case 1 文本与预期不符"
open(p,'w',encoding='utf-8').write(s)
print("M4 applied")
PY
run_gate M4 autoroute 'TestRoleLayerConstantsMatchAuditFieldValues'
restore

# ---- M5: 删掉 role_routing_active gauge 注册 ----------------------------
echo; echo "=========== M5: 不注册 role_routing_active gauge（构造函数） ==========="
snapshot
python3 - <<'PY'
p='autoroute/metrics.go'
s=open(p,encoding='utf-8').read()
before=s
s=s.replace('\treg.MustRegister(layers, active)\n','\treg.MustRegister(layers)\n',1)
assert s!=before, "M5 未命中：MustRegister 行文本与预期不符"
open(p,'w',encoding='utf-8').write(s)
print("M5 applied")
PY
run_gate M5 autoroute 'TestRoleFallbackMetricNamesMatchAlertRule'
restore

echo; echo "=========== POST-CONTROL（还原后必须全绿）==========="
( cd autoroute && go test ./ -count=1 -run 'TestRoleFallback|TestRecordRoleFallbackLayer|TestRoleLayer' 2>&1 | tail -3 )
( cd deploy/prometheus/rules && go test ./ -count=1 -run 'TestRoleFallback' 2>&1 | tail -3 )

echo; echo "=========== 残留检查 ==========="
leftover=$(ls autoroute/*.mutbak deploy/prometheus/rules/*.mutbak 2>/dev/null | wc -l | tr -d ' ')
echo "残留 .mutbak 数量: $leftover"
rm -rf "$BAKDIR"
