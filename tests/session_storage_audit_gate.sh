#!/usr/bin/env bash
# session_storage_audit_gate.sh — 让 TestStorageAudit* 集成审计套件成为可复跑门
# (r1031 §五-3 收口 / 判据纪律第 11 条「从未运行的判据等于没有判据」)。
#
# 背景:三域存储契约(冷读一致性 / 跨实例一致性 / V2 元数据与 body 配对)的唯一
# 观测器是 TestStorageAudit* 族,它们带 `-tags=integration` 且需要一次性回环
# Redis+PG——`go test ./...` 从不编译它们,此前无任何 CI 入口真实执行。
# 2026-10-14 首次真实执行即抓到真缺陷 F3(陈旧 Redis 元数据把 L2 复水塌缩成
# 空 body,lineage 重置为全量重发),同轮修复。
#
# 行为:docker daemon 与审计镜像(postgres:17-alpine / redis:7.4.11-alpine)可用
# 时必跑 scripts/audit/run-session-storage-stream.sh;不可用时拒绝通过。
# 默认要求跨实例 coherence，不允许把「只观察到不一致」解释成发布成功。
# 失败传播:套件 rc≠0 时本门非零退出(runner 内部 go test -count=1,无缓存绿)。
set -euo pipefail
cd "$(dirname "$0")/.."

if ! command -v docker >/dev/null 2>&1; then
  echo "[session-storage-audit] FAIL: docker 不可用 (需 docker 与 postgres:17-alpine / redis:7.4.11-alpine)" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "[session-storage-audit] FAIL: docker daemon 未运行" >&2
  exit 1
fi
if ! docker image inspect postgres:17-alpine redis:7.4.11-alpine >/dev/null 2>&1; then
  echo "[session-storage-audit] FAIL: 审计镜像缺失 (需 postgres:17-alpine / redis:7.4.11-alpine)" >&2
  exit 1
fi

bash scripts/audit/run-session-storage-stream.sh --require-coherence "$@"
