#!/usr/bin/env bash
# 部署前的**产物门**：确认新构建的二进制里真的有这些改动。
#
# 2026-10-06 起因：连续六轮修的都是「已实现 ≠ 已接线 / 核心动作从未执行」，
# 而验证手段一直是**包级**测试。包级全绿**证明不了**改动进了可运行产物。
# 本会话之前从未构建过真正的 gateway 二进制。
#
# 为什么值得单独一个脚本：`go test` 量的是「函数被调用时行为对不对」，
# `strings` 量的是「这段代码**在不在**产物里」。两者不可互相代替 ——
# 典型反例见下方 T3：Convention 机制全套判据都绿，但生产侧**没有任何调用方**，
# 于是 `prompt_excludes_cache` 在产物里是 0。
#
# 用法：
#   scripts/verify-build-contents.sh [binary]      # 默认 /tmp/gwbin/gateway
# 退出码 0 = 全部命中；1 = 有缺失（不要部署）。
set -uo pipefail

BIN="${1:-/tmp/gwbin/gateway}"
if [ ! -f "$BIN" ]; then
  echo "build-gate: 二进制不存在: $BIN" >&2
  echo "  先构建: go build -o '$BIN' ./cmd/gateway" >&2
  exit 1
fi

echo "build-gate: $BIN ($(wc -c < "$BIN" | tr -d ' ') bytes)"

fail=0

# ── 必须存在：16 条健康检查的 check_id ────────────────────────────────
# 少一条 = 那个缺陷在部署后不会被看见。顺序即 AllHealthChecks 的顺序。
#
# ★ 这份清单**必须**与 bg/routing_health_checks.go 的 AllHealthChecks() 完全一致，
#   少一条和多一条都要红 —— 由 bg/pricing_plan_stale_realdb_test.go 同文件的
#   TestEveryCheckIDIsInTheBuildManifest 钉住。
#   只做「清单里的都在」这一个方向时，新增一条检查而忘了登记是**静默**的：
#   脚本照样绿，而那条检查在部署后**没有任何产物门守着**。
#   （2026-10-06 加 pricing_plan_stale 时就是这么漏的，是判据在同一次改动里抓到的。）
CHECKS="canonical_id_null billing_mismatch probe_missing family_unknown circuit_open
        credential_active_not_routable modality_verification_stale baseline_observation_stale
        pricing_plan_stale
        baseline_price_missing supplier_price_drift supplier_price_currency_mismatch
        modality_gate_readiness_floor supplier_price_missing_from_cost
        recorded_cost_is_negative stats_ground_truth_gap
        offer_price_looks_like_placeholder canonical_row_discovered_but_never_referenced"

echo "── 健康检查 check_id ──"
n_checks=0
for c in $CHECKS; do
  n=$(strings -a "$BIN" | grep -c "$c" || true)
  if [ "$n" -eq 0 ]; then
    echo "  ✗ $c  —— 不在产物里，这条检查部署后不会跑"
    fail=1
  else
    echo "  ✓ $c ($n)"
  fi
  n_checks=$((n_checks + 1))
done
echo "  （共 $n_checks 条；本仓期望 15）"

# ── 必须存在：接线与关键日志文案 ──────────────────────────────────────
# 日志文案是**运维可判读**的证据。少一条 = 那条修复在部署后不产生任何痕迹。
echo "── 接线与日志文案 ──"
for s in \
  "authoritative URSM v2 modality_verification started" \
  "authoritative URSM v2 baseline_price_sync started" \
  "authoritative URSM v2 baseline_reconciliation started" \
  "modality_verification: started" \
  "cost computation negative" \
  "assumed_convention" \
  "not importable" \
  "rejected_rows" \
  "modality_source" \
  "modality_verified_at" \
  "modality_evidence" \
  "prompt_includes_cache" \
  "prompt_excludes_cache" \
  ; do
  n=$(strings -a "$BIN" | grep -c "$s" || true)
  if [ "$n" -eq 0 ]; then
    echo "  ✗ '$s'  —— 不在产物里"
    fail=1
  else
    echo "  ✓ '$s' ($n)"
  fi
done

# ── 必须为 0：已确认的死代码标记 ──────────────────────────────────────
# provider.Candidate.CalcCost 是死代码（零调用者）且语义与活的那份**相反**。
# 它的函数名不该出现在产物里。出现了说明有人重新接上了它 —— 那需要复核
# 语义（零价 return 0 vs return nil；负值截 0 vs 返回 nil）。
echo "── 应当缺席的东西 ──"
for s in "Candidate.CalcCost"; do
  n=$(strings -a "$BIN" | grep -c "$s" || true)
  if [ "$n" -ne 0 ]; then
    echo "  ✗ '$s' 出现了 ($n) —— 死代码被重新接上了？它的语义与 domains/streaming.CalcCost 相反"
    fail=1
  else
    echo "  ✓ '$s' 确实不在（死代码仍死）"
  fi
done

echo
if [ "$fail" -ne 0 ]; then
  echo "build-gate: **不通过** —— 产物里缺少上列内容，不要部署。"
  exit 1
fi
echo "build-gate: 通过 —— 产物包含本轮全部改动。"
exit 0
