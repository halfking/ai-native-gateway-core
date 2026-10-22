#!/usr/bin/env bash
# R12 raw sink rollback rehearsal.
# The caller must provide the restart command; this script never guesses a
# production service or restarts a process implicitly.
set -euo pipefail

ENV_FILE=""
LOG_DIR=""
RESTART_CMD=""
HEALTH_CMD=""
DRY_RUN=false

usage() {
  cat <<'EOF'
Usage: r12-raw-sink-rollback.sh --env-file FILE --log-dir DIR --restart-cmd CMD [options]

Required:
  --env-file FILE       dotenv file containing LLM_GATEWAY_RAW_LOG_SINK
  --log-dir DIR         raw audit JSONL directory
  --restart-cmd CMD     explicit restart command (quoted shell command)
Options:
  --health-cmd CMD      explicit health check after restart
  --dry-run             print changes/commands without editing or restarting
  -h, --help            show this help
EOF
}

while (($#)); do
  case "$1" in
    --env-file) ENV_FILE=$2; shift 2 ;;
    --log-dir) LOG_DIR=$2; shift 2 ;;
    --restart-cmd) RESTART_CMD=$2; shift 2 ;;
    --health-cmd) HEALTH_CMD=$2; shift 2 ;;
    --dry-run) DRY_RUN=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[[ -n "$ENV_FILE" && -n "$LOG_DIR" && -n "$RESTART_CMD" ]] || { usage >&2; exit 2; }
[[ -f "$ENV_FILE" ]] || { echo "env file not found: $ENV_FILE" >&2; exit 1; }
[[ -d "$LOG_DIR" ]] || { echo "log directory not found: $LOG_DIR" >&2; exit 1; }

old_mode=$(awk -F= '$1 == "LLM_GATEWAY_RAW_LOG_SINK" {print $2; found=1} END {if (!found) print "legacy"}' "$ENV_FILE" | tail -1)
printf 'R12 rollback: current=%s target=legacy dry_run=%s\n' "${old_mode:-legacy}" "$DRY_RUN"

if [[ "$DRY_RUN" == true ]]; then
  printf 'would set LLM_GATEWAY_RAW_LOG_SINK=legacy in %s\n' "$ENV_FILE"
  printf 'would run restart: %s\n' "$RESTART_CMD"
  [[ -n "$HEALTH_CMD" ]] && printf 'would run health check: %s\n' "$HEALTH_CMD"
  printf 'would validate JSONL files under %s\n' "$LOG_DIR"
  exit 0
fi

backup=$(mktemp "${ENV_FILE}.r12-backup.XXXXXX")
cp "$ENV_FILE" "$backup"
rollback_on_error=true
cleanup() {
  if [[ "$rollback_on_error" == true ]]; then
    cp "$backup" "$ENV_FILE"
  fi
  rm -f "$backup"
}
trap cleanup EXIT

if grep -q '^LLM_GATEWAY_RAW_LOG_SINK=' "$ENV_FILE"; then
  sed -i 's/^LLM_GATEWAY_RAW_LOG_SINK=.*/LLM_GATEWAY_RAW_LOG_SINK=legacy/' "$ENV_FILE"
else
  printf '\nLLM_GATEWAY_RAW_LOG_SINK=legacy\n' >> "$ENV_FILE"
fi
printf 'set LLM_GATEWAY_RAW_LOG_SINK=legacy\n'

eval "$RESTART_CMD"
if [[ -n "$HEALTH_CMD" ]]; then
  eval "$HEALTH_CMD"
fi

python_cmd="${PYTHON:-python3}"
if command -v "$python_cmd" >/dev/null 2>&1 && "$python_cmd" -c 'pass' >/dev/null 2>&1; then
  "$python_cmd" - "$LOG_DIR" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
files = sorted(root.glob('raw_data_*.jsonl'))
if not files:
    raise SystemExit(f'no raw_data_*.jsonl files under {root}')
rows = 0
for path in files:
    with path.open(encoding='utf-8') as fh:
        for lineno, line in enumerate(fh, 1):
            if not line.strip():
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError as exc:
                raise SystemExit(f'invalid JSONL {path}:{lineno}: {exc}')
            if not isinstance(value, dict):
                raise SystemExit(f'non-object JSONL {path}:{lineno}')
            rows += 1
print(f'JSONL continuity PASS files={len(files)} rows={rows}')
PY
elif command -v perl >/dev/null 2>&1; then
  perl -MJSON::PP -MFile::Find -e '
    my $root = shift @ARGV;
    my @files;
    find(sub { push @files, $File::Find::name if -f $_ && /^raw_data_.*\.jsonl$/ }, $root);
    die "no raw_data_*.jsonl files under $root\n" unless @files;
    my $rows = 0;
    for my $path (sort @files) {
      open my $fh, "<", $path or die "open $path: $!\n";
      my $line_no = 0;
      while (my $line = <$fh>) {
        ++$line_no;
        next if $line =~ /^\s*$/;
        my $value = eval { decode_json($line) };
        die "invalid JSONL $path:$line_no\n" if $@;
        die "non-object JSONL $path:$line_no\n" unless ref($value) eq "HASH";
        ++$rows;
      }
      close $fh or die "close $path: $!\n";
    }
    print "JSONL continuity PASS files=" . scalar(@files) . " rows=$rows\n";
  ' "$LOG_DIR"
else
  echo "python3 or perl is required for JSONL validation" >&2
  exit 1
fi

printf 'R12 rollback rehearsal PASS: legacy restart and JSONL continuity verified\n'
rollback_on_error=false
