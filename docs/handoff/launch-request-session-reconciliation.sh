#!/usr/bin/env bash
set -euo pipefail
umask 077

workspace="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
model="${RECONCILIATION_CODEX_MODEL:-gpt-6-luna}"
skill="${RECONCILIATION_HANDOFF_SKILL:-${CODEX_HOME:-${HOME}/.codex}/skills/handoff}"
audit='docs/audit/2026-11-23-请求会话统计与内外对账审计及实施规划.md'
launcher='docs/handoff/launch-request-session-reconciliation.sh'

lanes=(
  'A 出站账。复核流式、非流式、重试、竞速、后台直连和运维各入口的发送事实、费用和响应证据。交付入口矩阵及最小增量设计。'
  'B 入站和内部账。复核早拒、取消、会话冷热关联、平台归属、积分过账及冲正。交付关联键、状态矩阵及样例。'
  'C 报表。核对报告查询、导出、授权、时区、水位和桌面及移动帮助入口。交付报表到数据源的矩阵及验收清单。'
)

case "${1:---dry-run}" in
  --help|-h)
    cat <<'USAGE'
用法：bash launch-request-session-reconciliation.sh [--dry-run|--handoff|--preflight|--implement]
  --dry-run    显示预检计划（默认）
  --handoff    生成并校验 handoff 文档及提示词
  --preflight  启动三条并行的只读 Codex 核对
  --implement  启动一个工作区可写的实施协调会话
可用 RECONCILIATION_CODEX_MODEL 指定环境支持的低成本模型。
可用 RECONCILIATION_HANDOFF_SKILL 指定 handoff 技能目录。
USAGE
    exit 0
    ;;
  --dry-run)
    printf '单次预检包含三条并行只读任务。模型 %s\n' "${model}"
    printf '%s\n' "${lanes[@]}"
    exit 0
    ;;
  --handoff|--preflight|--implement)
    mode="$1"
    ;;
  *)
    printf '未知参数 %s。使用 --help 查看用法。\n' "$1" >&2
    exit 2
    ;;
esac

test -f "${workspace}/${audit}"
if [[ ! -f "${skill}/scripts/handoff.py" ]]; then
  printf '找不到 handoff.py。请设置 RECONCILIATION_HANDOFF_SKILL。\n' >&2
  exit 1
fi
if [[ "${mode}" != --handoff ]]; then
  command -v codex >/dev/null || { printf '找不到 Codex CLI。\n' >&2; exit 1; }
fi

run_dir="$(mktemp -d "${TMPDIR:-/tmp}/request-session-reconciliation.XXXXXX")"
summary="${run_dir}/summary.json"
python3 - "${summary}" "${workspace}/${audit}" "${workspace}/${launcher}" <<'PY'
import json
import sys

summary = {
    "task": "请求与会话统计及内外对账实施",
    "mission": "基于当前代码审计，建立逐次供应商调用对账、客户端请求及内部实际扣费的统一统计口径。",
    "focus": "先冻结两种事件粒度、三种金额、平台运维身份和报表验收样例。",
    "approach": "复用现有请求遥测、内部账本、对账快照和帮助入口，按 A 出站、B 入站内账、C 报表三线并行。",
    "done": ["已形成代码审计、统计口径表及实施顺序；仓库现有桌面报表帮助已接入共用定义。"],
    "current": "实施前预检。逐次账本、统一报表查询及移动端帮助待实施。",
    "last_action": "校验本轮报表帮助并生成启动材料。",
    "pending": ["合同费率与账单样本、历史覆盖率、空库与历史升级夹具、影子对账和切换。"],
    "next_steps": [
        "阅读审计规划。并行复核 A 出站、B 入站内账、C 报表，使用可用的低成本模型。",
        "协调人冻结键、接口和测试样例，分配写入归属与迁移号，再执行实现、集成和隔离验证。",
    ],
    "decisions": [
        "供应商发送、客户端请求及会话各按自身粒度计数；内部过账、供应商测算、供应商返回金额分列。",
        "平台自动化运维归属专用客户端，进入供应商成本与平台用量，排除租户扣费。",
    ],
    "risks": [
        "当前代码审计未使用供应商账单或生产重跑；历史上无法还原的费用必须标注缺失。",
        "工作区可能存在用户或其他任务的修改；保留它们。生产操作须使用独立授权。",
    ],
    "skills": [],
    "references": sys.argv[2:],
}
with open(sys.argv[1], "x", encoding="utf-8") as out:
    json.dump(summary, out, ensure_ascii=False, indent=2)
PY

python3 "${skill}/scripts/handoff.py" create --workspace "${workspace}" \
  --input "${summary}" --output-dir "${run_dir}"
prompts=("${run_dir}"/handoff-*.md.prompt.txt)
prompt="${prompts[0]}"
test -f "${prompt}"
if [[ "${mode}" == --handoff ]]; then
  exit 0
fi

if [[ "${mode}" == --implement ]]; then
  {
    cat "${prompt}"
    printf '\n这是实施协调会话。先做阶段 0，再按规划推进。并行子代理使用低成本模型 %s，模型不可用时选当前支持的低成本模型。协调共享接口和增量迁移，实施与验证仅限本机授权范围。\n' "${model}"
  } | codex exec --cd "${workspace}" --model "${model}" --sandbox workspace-write -
  exit 0
fi

run_lane() {
  {
    cat "${prompt}"
    printf '\n本轮仅做下列一条静态预检。不得改文件、连接数据库、访问生产服务或继续委派。\n%s\n' "$1"
  } | codex exec --cd "${workspace}" --model "${model}" --sandbox read-only --ephemeral -
}

pids=()
logs=()
for lane in "${lanes[@]}"; do
  log_file="${run_dir}/preflight-${#pids[@]}.log"
  run_lane "${lane}" >"${log_file}" 2>&1 &
  pids+=("$!")
  logs+=("${log_file}")
done

failed=0
for index in "${!pids[@]}"; do
  if wait "${pids[index]}"; then
    printf '完成 %s\n' "${logs[index]}"
  else
    printf '失败 %s\n' "${logs[index]}" >&2
    failed=1
  fi
done
exit "${failed}"
