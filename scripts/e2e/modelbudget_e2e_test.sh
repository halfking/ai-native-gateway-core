#!/usr/bin/env bash
# ★★ 2026-11-23（211 配置优化执行报告 §9.24 轮 · N4 基线红收口）：★★ 本脚本在【宿主】上
# ★★ 编译 Go ⇒ ⇒ 子进程会往 Go 的构建缓存里写产物 ⇒ ⇒ 而那个缓存默认是【用户那个全局的】
# ★★ ⇒ ⇒ 必须指向【专用缓存】（★ 目录约定与 6 GB 上限见 `scripts/lib/gocache.sh`；
# ★★ 理由与实测读数见 `internal/gocache` 的包注释 ⇒ ★ 实测：★ **单包** `go test ./admin/`
# ★★ 就要 **846 MB** ⇒ ⇒ 体积由【传递依赖闭包】决定 ⇒ ⇒ 按「编译范围」分层不成立）。
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
# P1#4 Phase 2 e2e 可执行入口 —— 任务级预算(max_cost_per_task)五场景
# 形态:进程内 httptest(真实 ChatHandler 管线,capture executor 承接上游),
# 详见 docs/distributed-agent-system/evidence/phase2/p1-4-contract-verification-report.md §5.3
set -e
cd "$(dirname "$0")/../.."
echo "=== P1#4 ModelBudget E2E(进程内形态) ==="
echo "场景1 预算充足透传 / 场景2 紧张降级gpt-4-turbo / 场景2b 0.001落gpt-4o-mini / 场景3 低于0.0006015→402 / 场景4 无字段向后兼容"
go test ./domains/streaming/ -count=1 -run "TestModelBudgetE2E" -v "$@"
echo "=== All scenarios passed ==="
