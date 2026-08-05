#!/bin/bash
# docs/全方面测试/scenarios/S21_branch_session.sh
#
# S21: 分支会话 (Branch Session) — PLACEHOLDER
#
# 背景: 当前 llm-gateway-go **未实现** 分支会话 (fork / sub-session) 特性.
# 经 Phase 1 探索验证 (2026-08-06):
#   - sessions / session_titles / session_state_snapshots 等表均无 parent/branch 字段
#   - 没有任何 fork / branch / sub_session 路由
#   - chatHandler 没有 session fork 钩子
#   - docs/全方面测试/ 不存在 S21 相关场景
#
# 实施分支会话需要 (本测试不实现, 仅标记 TODO):
#   1. 新建 session_branches 表:
#        CREATE TABLE session_branches (
#            parent_session_id    TEXT NOT NULL,
#            child_session_id     TEXT NOT NULL,
#            child_turn_no        INTEGER NOT NULL DEFAULT 0,
#            branch_reason       TEXT,
#            created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
#            PRIMARY KEY (parent_session_id, child_session_id)
#        );
#   2. chatHandler 增加 fork 钩子, 启动时按 parent_session_id 从
#        request_logs_bodies 复制 last N turn
#   3. 新建 POST /v1/sessions/:id/fork 路由
#   4. session_v2 加 parent_session_id 列 (与 session_titles 复合 PK 类似)
#   5. admin API: POST /api/admin/sessions/<id>/fork
#
# 实施完后再回填本场景, 覆盖:
#   21.1 basic fork: 父 session A (3 turns) → 子 session B, B 第 1 轮应含 A 完整历史
#   21.2 sibling: A fork 两次 (B + C), B/C 共享 A 历史但 turn 独立
#   21.3 跨 fork 互不影响: B/C 后 A 仍 3 turns
#   21.4 tenant 隔离: 跨租户 fork 应 404/403
#   21.5 不可见 session: 无权限时不能 fork 别人的 session
#
# 2026-08-06: 首次创建 (placeholder, exit 0)

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/_lib.sh"

SCENARIO="S21_branch_session"
RESULT="$RESULTS_DIR/${SCENARIO}.json"
mkdir -p "$RESULTS_DIR"

log() { echo "[S21] $*"; }

log "S21: branch session placeholder"
log "特性未实现 (session_branches 表未建立, POST /v1/sessions/:id/fork 路由未实现)"
log "see docs/全方面测试/分支会话设计-TODO.md (待编写)"

# 写结果 (SKIPPED, exit 0)
cat > "$RESULT" <<'EOF'
{
  "scenario": "S21_branch_session",
  "status": "SKIPPED",
  "reason": "feature not implemented",
  "design_todo": [
    "CREATE TABLE session_branches (parent_session_id, child_session_id, child_turn_no, branch_reason, created_at)",
    "POST /v1/sessions/:id/fork 路由",
    "chatHandler fork 钩子, 从 request_logs_bodies 复制 last N turn",
    "session_v2 加 parent_session_id 列",
    "admin API: POST /api/admin/sessions/<id>/fork"
  ],
  "tests_todo": [
    "21.1 basic_fork_3turns",
    "21.2 sibling_shares_history",
    "21.3 cross_fork_isolation",
    "21.4 tenant_isolation_404",
    "21.5 forbidden_fork_403"
  ]
}
EOF

skip_scenario "branch session not implemented (see S21_branch_session.sh header)"
