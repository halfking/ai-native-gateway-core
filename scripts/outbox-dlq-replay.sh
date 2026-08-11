#!/usr/bin/env bash
# scripts/outbox-dlq-replay.sh
#
# DLQ (Dead Letter Queue) management tool for outbox_events.
# Allows querying, replaying, and marking DLQ events as processed.
#
# Usage:
#   ./scripts/outbox-dlq-replay.sh list [--limit N]
#   ./scripts/outbox-dlq-replay.sh show <event_id>
#   ./scripts/outbox-dlq-replay.sh replay <event_id>
#   ./scripts/outbox-dlq-replay.sh replay-all [--dry-run]
#   ./scripts/outbox-dlq-replay.sh mark-processed <event_id>
#   ./scripts/outbox-dlq-replay.sh purge --older-than DAYS
#
# Prerequisites:
#   - DATABASE_URL environment variable (or pass --db-url)
#   - psql installed

set -euo pipefail

# Configuration
DATABASE_URL="${DATABASE_URL:-}"
DB_URL_ARG=""
DRY_RUN=false

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Functions
usage() {
    cat <<EOF
Usage: $0 COMMAND [OPTIONS]

DLQ (Dead Letter Queue) management tool for outbox_events.

Commands:
  list [--limit N]              List all DLQ events (default limit 50)
  show <event_id>               Show details of a specific event
  replay <event_id>             Replay a single DLQ event
  replay-all [--dry-run]        Replay all DLQ events
  mark-processed <event_id>     Mark event as processed (won't retry)
  purge --older-than DAYS       Purge DLQ events older than N days

Options:
  --db-url URL                  Database connection URL (default: \$DATABASE_URL)
  --dry-run                     Show what would be done without executing
  -h, --help                    Show this help message

Examples:
  # List all DLQ events
  $0 list

  # Show details of a specific event
  $0 show evt-2026-08-11-abc123

  # Replay a single event (reset status to pending, attempts to 0)
  $0 replay evt-2026-08-11-abc123

  # Dry-run replay all DLQ events
  $0 replay-all --dry-run

  # Mark event as processed (move to 'processed' status)
  $0 mark-processed evt-2026-08-11-abc123

  # Purge DLQ events older than 30 days
  $0 purge --older-than 30

Environment:
  DATABASE_URL    PostgreSQL connection URL (required)
EOF
    exit 1
}

log_info() {
    echo -e "${BLUE}[INFO]${NC} $*"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $*"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

check_db_url() {
    if [[ -z "$DATABASE_URL" && -z "$DB_URL_ARG" ]]; then
        log_error "DATABASE_URL environment variable not set and --db-url not provided"
        exit 1
    fi
    
    if [[ -n "$DB_URL_ARG" ]]; then
        DATABASE_URL="$DB_URL_ARG"
    fi
}

psql_exec() {
    psql "$DATABASE_URL" -t -A "$@"
}

cmd_list() {
    local limit=50
    
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --limit)
                limit="$2"
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done
    
    check_db_url
    
    log_info "Listing DLQ events (limit: $limit)..."
    
    psql_exec -c "
        SELECT 
            event_id,
            tenant_id,
            aggregate_id,
            attempts,
            LEFT(last_error, 50) AS error_preview,
            created_at::date AS created
        FROM outbox_events
        WHERE status = 'dlq'
        ORDER BY created_at DESC
        LIMIT $limit
    " | column -t -s '|'
    
    local count
    count=$(psql_exec -c "SELECT COUNT(*) FROM outbox_events WHERE status = 'dlq'")
    log_info "Total DLQ events: $count"
}

cmd_show() {
    if [[ $# -ne 1 ]]; then
        log_error "Missing event_id argument"
        usage
    fi
    
    local event_id="$1"
    check_db_url
    
    log_info "Fetching event: $event_id"
    
    psql_exec -c "
        SELECT 
            event_id,
            event_type,
            schema_version,
            tenant_id,
            aggregate_id,
            aggregate_version,
            occurred_at,
            status,
            attempts,
            last_error,
            next_retry_at,
            created_at,
            updated_at,
            payload
        FROM outbox_events
        WHERE event_id = '$event_id'
    " -x
}

cmd_replay() {
    if [[ $# -ne 1 ]]; then
        log_error "Missing event_id argument"
        usage
    fi
    
    local event_id="$1"
    check_db_url
    
    # Verify event exists and is in DLQ
    local status
    status=$(psql_exec -c "SELECT status FROM outbox_events WHERE event_id = '$event_id'")
    
    if [[ -z "$status" ]]; then
        log_error "Event not found: $event_id"
        exit 1
    fi
    
    if [[ "$status" != "dlq" ]]; then
        log_error "Event is not in DLQ (current status: $status)"
        exit 1
    fi
    
    log_info "Replaying event: $event_id"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_warn "DRY RUN: Would reset $event_id to pending with attempts=0"
    else
        psql_exec -c "
            UPDATE outbox_events
            SET status = 'pending', attempts = 0, last_error = NULL, next_retry_at = NULL, updated_at = NOW()
            WHERE event_id = '$event_id'
        " > /dev/null
        log_success "Event $event_id reset to pending"
    fi
}

cmd_replay_all() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done
    
    check_db_url
    
    local count
    count=$(psql_exec -c "SELECT COUNT(*) FROM outbox_events WHERE status = 'dlq'")
    
    if [[ "$count" -eq 0 ]]; then
        log_info "No DLQ events to replay"
        exit 0
    fi
    
    log_warn "About to replay $count DLQ events"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_warn "DRY RUN: Would reset $count events to pending"
    else
        read -p "Continue? (yes/no) " -r
        if [[ ! $REPLY =~ ^[Yy]es$ ]]; then
            log_info "Aborted"
            exit 0
        fi
        
        psql_exec -c "
            UPDATE outbox_events
            SET status = 'pending', attempts = 0, last_error = NULL, next_retry_at = NULL, updated_at = NOW()
            WHERE status = 'dlq'
        " > /dev/null
        log_success "Reset $count events to pending"
    fi
}

cmd_mark_processed() {
    if [[ $# -ne 1 ]]; then
        log_error "Missing event_id argument"
        usage
    fi
    
    local event_id="$1"
    check_db_url
    
    log_info "Marking event as processed: $event_id"
    
    if [[ "$DRY_RUN" == "true" ]]; then
        log_warn "DRY RUN: Would mark $event_id as processed"
    else
        psql_exec -c "
            UPDATE outbox_events
            SET status = 'processed', updated_at = NOW()
            WHERE event_id = '$event_id' AND status = 'dlq'
        " > /dev/null
        log_success "Event $event_id marked as processed"
    fi
}

cmd_purge() {
    local days=""
    
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --older-than)
                days="$2"
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                usage
                ;;
        esac
    done
    
    if [[ -z "$days" ]]; then
        log_error "Missing --older-than DAYS argument"
        usage
    fi
    
    check_db_url
    
    local count
    count=$(psql_exec -c "
        SELECT COUNT(*) FROM outbox_events
        WHERE status = 'dlq' AND created_at < NOW() - INTERVAL '$days days'
    ")
    
    if [[ "$count" -eq 0 ]]; then
        log_info "No DLQ events older than $days days to purge"
        exit 0
    fi
    
    log_warn "About to purge $count DLQ events older than $days days"
    read -p "Continue? (yes/no) " -r
    if [[ ! $REPLY =~ ^[Yy]es$ ]]; then
        log_info "Aborted"
        exit 0
    fi
    
    psql_exec -c "
        DELETE FROM outbox_events
        WHERE status = 'dlq' AND created_at < NOW() - INTERVAL '$days days'
    " > /dev/null
    log_success "Purged $count old DLQ events"
}

# Main
if [[ $# -eq 0 ]]; then
    usage
fi

# Parse global options
while [[ $# -gt 0 ]]; do
    case "$1" in
        --db-url)
            DB_URL_ARG="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        list|show|replay|replay-all|mark-processed|purge)
            COMMAND="$1"
            shift
            break
            ;;
        *)
            log_error "Unknown command: $1"
            usage
            ;;
    esac
done

case "${COMMAND:-}" in
    list)
        cmd_list "$@"
        ;;
    show)
        cmd_show "$@"
        ;;
    replay)
        cmd_replay "$@"
        ;;
    replay-all)
        cmd_replay_all "$@"
        ;;
    mark-processed)
        cmd_mark_processed "$@"
        ;;
    purge)
        cmd_purge "$@"
        ;;
    *)
        usage
        ;;
esac
