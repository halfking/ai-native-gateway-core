#!/usr/bin/env bash
# ★★ r1048 轮（★ 2026-10-29）：★ 本脚本在【宿主】上编译 Go ⇒ ⇒ 子进程会往 Go 的
# ★★ 构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】⇒ ⇒ 必须指向【专用缓存】
# ★★ （★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；★ 理由与实测读数见
# ★★ `internal/gocache` 的包注释 ⇒ ⇒ ★★ 实测：★ **单包** `go test ./admin/` 就要
# ★★ **846 MB** ⇒ ⇒ ★★ 体积由【传递依赖闭包】决定 ⇒ ⇒ ★★ 按「编译范围」分层不成立）。
# ★★ 【失败安全】：★ 找不到共享入口 ⇒ ⇒ **不设 GOCACHE**（★ 宁可漏隔离 ⇒ ⇒ 也不要把
# ★★ GOCACHE 设成垃圾值 ⇒ ⇒ 那会让 go 直接报错 ⇒ ⇒ 把脚本弄坏）。
__gocache_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd)" || __gocache_dir=""
while [ -n "$__gocache_dir" ] && [ ! -f "$__gocache_dir/scripts/lib/gocache.sh" ] && [ "$__gocache_dir" != "/" ]; do
  __gocache_dir="$(dirname "$__gocache_dir")"
done
if [ -n "$__gocache_dir" ] && [ -f "$__gocache_dir/scripts/lib/gocache.sh" ]; then
  . "$__gocache_dir/scripts/lib/gocache.sh"
  gocache_use script
fi
# collect-query-plan-observations.sh —— R179 轮：**实测计划形态**观测台账的采集入口。
#
# # 这道脚本在钉什么
#
# ★ 「索引存在」≠「索引被用上」⇒ ⇒ 要回答「规划器实际走了哪条路」，
# ★ 就得在**目标环境**上把真实语句 EXPLAIN 一遍。
#
# ★ 流水线（三段，各自有独立的事实源）：
#   ① scripts/audit/collect-canonical-statements.sh
#      ⇒ 从 252 的 pg_stat_statements 采「生产上真的在跑的语句」（★ 含调用次数与平均耗时）
#   ② scripts/audit/build-plan-form-spec.py
#      ⇒ 把那些语句变成【实测 spec】（★ 每条语句的参数角色 + 只取「读视图的那段 SELECT」）
#   ③ cmd/tools/query_plan_form
#      ⇒ 在目标环境上 EXPLAIN ⇒ 读出**计划形态**（★ seek / filter / seq-scan / no-scan）
#      ⇒ ⇒ 落成 scripts/audit/asset/query-plan-observations.tsv
#
# ★★ 两个必须记住的口径 ★★
#   ① **不带 ANALYZE** ⇒ ★ 只读库上**不执行**查询（★ 安全，且不制造慢 SQL）；
#   ② **代表值必须语义有效** ⇒ ★ 工具用 `row_to_json` 从**目标环境真实的一行**取该列的值
#      ⇒ ★ 若某个列角色拿不到有效值 ⇒ ★ 该条读数**作废**（★ plan_form=error）
#      ⇒ ★ 理由：★ `success = NULL` 会让规划器**把谓词优化掉** ⇒ ⇒ 读出来是假的 no-scan。
#
# 用法：
#   bash scripts/audit/collect-query-plan-observations.sh            # ★ 采集 + 落盘
#   bash scripts/audit/collect-query-plan-observations.sh --local    # ★ 本地环境（自检用）

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO}"

ENV_NAME="${KX_PLANFORM_ENV:-252}"
DSN_FILE="${KX_PLANFORM_DSN_FILE:-/tmp/kx-local-dsn-host.txt}"
VIEW="request_logs_with_current_month"
SPEC="cmd/tools/query_plan_form/specs/r179-canonical-statements.json"
OUT="scripts/audit/asset/query-plan-observations.tsv"

if [[ "${1:-}" == "--local" ]]; then
	ENV_NAME="local"
fi

echo "★ ① 采集生产语句（★ ${ENV_NAME}）"
bash scripts/audit/collect-canonical-statements.sh > scripts/audit/asset/canonical-view-statements.tsv

echo "★ ② 构建实测 spec"
python3 scripts/audit/build-plan-form-spec.py \
	--statements scripts/audit/asset/canonical-view-statements.tsv \
	--source-shapes-json scripts/audit/asset/canonical-read-point-shapes.json \
	--out-spec "${SPEC}" \
	--out-map /tmp/r179-statement-map.tsv

echo "★ ③ 实测计划形态"
if [[ "${ENV_NAME}" == "local" ]]; then
	go run ./cmd/tools/query_plan_form/ -spec "${SPEC}" -out "${OUT}" -env local \
		-dsn-file "${DSN_FILE}" -view "${VIEW}"
else
	go run ./cmd/tools/query_plan_form/ -spec "${SPEC}" -out "${OUT}" -env "${ENV_NAME}" \
		-view "${VIEW}"
fi

echo "★ 完成 ⇒ ${OUT}"
