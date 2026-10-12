#!/usr/bin/env bash
# =============================================================================
# sync-gateway-db-to-211.sh — 把本机 pg 的 llm_gateway 库灌进 211 上的 docker PG
#
# 流程：pg_dump（自定义格式，可并行/可续）→ ssh 管道 → 211 上 pg_restore 进容器。
# 全程不落地明文 dump 文件（dump 含凭据哈希与业务数据），走 stdin 管道。
#
# 目标库由 deploy-211-docker.sh 决定（可能复用 211 上已有的 pg17+columnar 实例），
# 本脚本从 211 上的 /opt/llm-gateway-go-211/env/gateway.env 读连接信息，
# 因此**必须先跑完 deploy-211-docker.sh**。
#
# 用法（本机执行）：
#   bash scripts/sync-gateway-db-to-211.sh                 # 预览（只对比，不写）
#   bash scripts/sync-gateway-db-to-211.sh --apply         # 真灌
#   bash scripts/sync-gateway-db-to-211.sh --apply --reset # 先 DROP 再灌（干净替换）
#   bash scripts/sync-gateway-db-to-211.sh --verify        # 灌完后的行数对账
#
# 本机源库（默认本机 host-first 部署那套）：
#   D211_SRC_HOST=127.0.0.1 D211_SRC_PORT=54318
#   D211_SRC_DB=llm_gateway D211_SRC_USER=<当前用户>
# =============================================================================
set -euo pipefail

D211_VIA="${D211_VIA:-192.168.31.211}"
D211_STATE_DIR="${D211_STATE_DIR:-/opt/llm-gateway-go-211}"
D211_SRC_HOST="${D211_SRC_HOST:-127.0.0.1}"
D211_SRC_PORT="${D211_SRC_PORT:-54318}"
D211_SRC_DB="${D211_SRC_DB:-llm_gateway}"
D211_SRC_USER="${D211_SRC_USER:-$(id -un)}"
D211_JOBS="${D211_JOBS:-4}"

log()  { printf '[sync-211] %s\n' "$*" >&2; }
die()  { printf '[sync-211] FATAL: %s\n' "$*" >&2; exit 1; }

MODE=preview
while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply)  MODE=apply; shift ;;
    --reset)  MODE=reset; shift ;;
    --verify) MODE=verify; shift ;;
    -h|--help) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

# 本机 psql/pg_dump 定位：go 与 pg 都不在默认 PATH 上
if ! command -v pg_dump >/dev/null 2>&1; then
  for d in "$HOME"/kaixuan/pg*/bin /opt/homebrew/opt/postgresql*/bin /usr/local/opt/postgresql*/bin; do
    [[ -x "$d/pg_dump" ]] && export PATH="$d:$PATH" && break
  done
fi
command -v pg_dump >/dev/null 2>&1 || die "本机找不到 pg_dump（装 postgresql client 或设 PATH）"
log "本机 pg_dump: $(pg_dump --version)"

src_psql() { psql -h "$D211_SRC_HOST" -p "$D211_SRC_PORT" -U "$D211_SRC_USER" -d "$D211_SRC_DB" "$@"; }

# ── 源库体检 ───────────────────────────────────────────────────────────
src_ok="$(src_psql -X -tAc 'SELECT 1' 2>/dev/null || true)"
[[ "$src_ok" == "1" ]] || die "连不上本机源库 ${D211_SRC_USER}@${D211_SRC_HOST}:${D211_SRC_PORT}/${D211_SRC_DB}"
SRC_SIZE="$(src_psql -X -tAc "SELECT pg_size_pretty(pg_database_size('$D211_SRC_DB'))")"
SRC_TABLES="$(src_psql -X -tAc "SELECT count(*) FROM pg_tables WHERE schemaname='public'")"
SRC_ROWS="$(src_psql -X -tAc "SELECT COALESCE(sum(n_live_tup),0)::bigint FROM pg_stat_user_tables")"
log "源库 ${D211_SRC_DB}@${D211_SRC_HOST}:${D211_SRC_PORT}  库大小=${SRC_SIZE}  public 表=${SRC_TABLES}  估算活行=${SRC_ROWS}"

# 目标：跑在 211 的 docker 容器里
remote_psql() { ssh "$D211_VIA" "docker exec -i $1 psql -X -U ${2:-postgres} -d ${3:-postgres} $*"; }

# 从 211 上的 env 文件解析出目标容器/库/用户（由 deploy 脚本写）
read_target() {
  local envf="/opt/llm-gateway-go-211/env/gateway.env"
  ssh "$D211_VIA" "cat $envf" > "$TMP_ENV" 2>/dev/null || die "读不到 $D211_VIA:${envf}（先跑 deploy-211-docker.sh）"
  # postgres://user:pass@container:5432/db?sslmode=disable
  local url; url="$(grep -oE 'postgres://[^ ]+' "$TMP_ENV" | head -1)"
  [[ -n "$url" ]] || die "env 文件里没有 LLM_GATEWAY_DATABASE_URL"
  TGT_CONTAINER="$(printf '%s' "$url" | sed -E 's#.*@([^:]+):.*#\1#')"
  TGT_DB="$(printf '%s' "$url" | sed -E 's#.*/([^/?]+).*#\1#')"
  TGT_PASS="$(printf '%s' "$url" | sed -E 's#.*://[^:]*:([^@]*)@.*#\1#')"
  log "目标 211 容器=${TGT_CONTAINER} 库=${TGT_DB}"
}

TMP_ENV="$(mktemp)"; trap 'rm -f "$TMP_ENV"' EXIT

if [[ "$MODE" == "preview" ]]; then
  log "--- 目标现状（不改动）---"
  read_target
  ssh "$D211_VIA" "docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER psql -X -U postgres -d $TGT_DB -tAc \"
    SELECT '目标库大小='||pg_size_pretty(pg_database_size('$TGT_DB'));
    SELECT '目标 public 表='||count(*) FROM pg_tables WHERE schemaname='public';
    SELECT '目标 schema_migrations='||count(*) FROM schema_migrations;\"" 2>&1 | grep -v '^$' || true
  log ""
  log "预览结束。确认无误后执行: --apply（追加恢复）或 --reset（先清空再恢复）"
  exit 0
fi

read_target

if [[ "$MODE" == "reset" ]]; then
  log "清空目标库（--reset）..."
  ssh "$D211_VIA" "docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER psql -X -U postgres -d postgres -c \
     \"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$TGT_DB';\"" >/dev/null 2>&1 || true
  ssh "$D211_VIA" "docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER psql -X -U postgres -d postgres -c \
     \"DROP DATABASE IF EXISTS \\\"$TGT_DB\\\"; CREATE DATABASE \\\"$TGT_DB\\\" OWNER \\\"llm_gateway\\\";\"" \
     || die "重建目标库失败"
fi

if [[ "$MODE" == "verify" ]]; then
  log "--- 对账 ---"
  for t in api_keys model_bindings provider_endpoints session_summaries; do
    s="$(src_psql -X -tAc "SELECT count(*) FROM $t" 2>/dev/null || echo '-')"
    d="$(ssh "$D211_VIA" "docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER psql -X -U postgres -d $TGT_DB -tAc 'SELECT count(*) FROM $t'" 2>/dev/null | tr -d '[:space:]' || echo '-')"
    printf '  %-24s 源=%-12s 目标=%s\n' "$t" "$s" "${d:-0}"
  done
  exit 0
fi

# ── 真灌 ───────────────────────────────────────────────────────────────
# 扩展先建好：pg_restore 遇到 CREATE EXTENSION 才建的话，顺序上会晚于表定义。
log "预建扩展 ..."
for ext in btree_gist pg_stat_statements pg_trgm pgcrypto pgstattuple vector pgcolumnar; do
  ssh "$D211_VIA" "docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER psql -X -U postgres -d $TGT_DB -c \
     \"CREATE EXTENSION IF NOT EXISTS $ext;\"" >/dev/null 2>&1 \
     && log "  ok   $ext" \
     || warn "  跳过 ${ext}（目标 PG 装不上；恢复时可能报错）"
done

log "pg_dump → pg_restore（${SRC_SIZE}，走 ssh 管道，不落盘）..."
set +e
pg_dump -h "$D211_SRC_HOST" -p "$D211_SRC_PORT" -U "$D211_SRC_USER" -d "$D211_SRC_DB" \
  --format=custom --compress=6 --no-owner --no-acl --jobs=1 \
| ssh "$D211_VIA" "cat > /tmp/gw.dump && docker cp /tmp/gw.dump $TGT_CONTAINER:/tmp/gw.dump && \
    docker exec -e PGPASSWORD='$TGT_PASS' $TGT_CONTAINER pg_restore -U postgres -d $TGT_DB \
      --no-owner --no-acl --jobs=$D211_JOBS --exit-on-error /tmp/gw.dump; \
    rc=\$?; docker exec $TGT_CONTAINER rm -f /tmp/gw.dump; exit \$rc"
rc=$?
set -e
if (( rc != 0 )); then
  warn "pg_restore 返回 ${rc}（可能是对象已存在等非致命项；用 --verify 对账）"
fi
log "恢复流程结束（rc=${rc}），执行 --verify 对账"