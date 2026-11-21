#!/usr/bin/env bash
# P1#4 Phase 2 e2e 可执行入口 —— 任务级预算(max_cost_per_task)五场景
# 形态:进程内 httptest(真实 ChatHandler 管线,capture executor 承接上游),
# 详见 docs/distributed-agent-system/evidence/phase2/p1-4-contract-verification-report.md §5.3
set -e
cd "$(dirname "$0")/../.."
echo "=== P1#4 ModelBudget E2E(进程内形态) ==="
echo "场景1 预算充足透传 / 场景2 紧张降级gpt-4-turbo / 场景2b 0.001落gpt-4o-mini / 场景3 低于0.0006015→402 / 场景4 无字段向后兼容"
go test ./domains/streaming/ -count=1 -run "TestModelBudgetE2E" -v "$@"
echo "=== All scenarios passed ==="
