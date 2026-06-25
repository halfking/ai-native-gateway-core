#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# scan-secrets.sh — SI-LLM-Gateway 敏感信息扫描器
# ─────────────────────────────────────────────────────────────────────────────
set -uo pipefail

MODE="normal"
PATHS=(".")
FORMAT="text"
TRACKED_ONLY=0
BASELINE_FILE=""
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
RULES_FILE="$SCRIPT_DIR/scan-secrets.config"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ -t 1 ]]; then
  RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'; GREEN=$'\033[0;32m'
  BLUE=$'\033[0;34m'; BOLD=$'\033[1m'; RESET=$'\033[0m'
else
  RED=""; YELLOW=""; GREEN=""; BLUE=""; BOLD=""; RESET=""
fi

REPO_ROOT_OVERRIDE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode=*)        MODE="${1#*=}" ;;
    --paths)         shift; PATHS=("$@"); break ;;
    --paths=*)       PATHS=("${1#*=}") ;;
    --format=*)      FORMAT="${1#*=}" ;;
    --baseline=*)    BASELINE_FILE="${1#*=}" ;;
    --tracked-only)  TRACKED_ONLY=1 ;;
    --repo-root=*)   REPO_ROOT_OVERRIDE="${1#*=}" ;;
    --help|-h) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; exit 2 ;;
  esac
  shift
done

if [[ -n "$REPO_ROOT_OVERRIDE" ]]; then
  REPO_ROOT="$REPO_ROOT_OVERRIDE"
fi

case "$MODE" in
  strict|normal|permissive) ;;
  *) echo "Invalid --mode: $MODE" >&2; exit 2 ;;
esac

RULES_CATEGORY=(); RULES_SEVERITY=(); RULES_DESC=(); RULES_PATTERN=(); RULES_IS_FILENAME=()

if [[ -f "$RULES_FILE" ]]; then
  while IFS='|' read -r rcat rsev rdesc rpat; do
    [[ -z "$rcat" || "$rcat" =~ ^[[:space:]]*# ]] && continue
    rpat="${rpat%$'\r'}"
    [[ -z "$rpat" ]] && continue
    echo "" | grep -iE -e "$rpat" >/dev/null 2>&1
    rc=$?
    if [[ $rc -eq 2 ]]; then
      echo "${YELLOW}⚠️  Skipping invalid regex: ${rpat}${RESET}" >&2
      continue
    fi
    RULES_CATEGORY+=("$rcat"); RULES_SEVERITY+=("$rsev"); RULES_DESC+=("$rdesc"); RULES_PATTERN+=("$rpat")
    if [[ "$rcat" == "SECRET_FILE" ]]; then RULES_IS_FILENAME+=("1"); else RULES_IS_FILENAME+=("0"); fi
  done < "$RULES_FILE"
else
  echo "${RED}ERROR${RESET}: Rules file not found: $RULES_FILE" >&2; exit 2
fi

WHITELIST_PATTERNS=(
  'NT 10\.0' 'example\.com' 'example\.org' '127\.0\.0\.1' '0\.0\.0\.0' 'localhost'
  'YOUR_API_KEY_HERE' '<REDACTED>' '<INTERNAL_IP_REDACTED>' '<REDACTED_PASSWORD>'
  '<REDACTED_HASH>' '__REDACTED_[A-Z_]+__' '__INTERNAL_[A-Z_]+__'
  'xxxxxxxx-xxxx-xxxx' 'placeholder'
)

EXCLUDE_DIRS=(".git" "node_modules" "vendor" "build" "dist" "out" "coverage"
  ".playwright-mcp" ".codegraph" ".cache" ".runtime" ".ruff_cache"
  ".pnpm-store" ".secrets" ".deploy" ".trash" ".artifacts" ".idea" ".vscode" ".cursor")
EXCLUDE_FILES=("scan-secrets.sh" "scan-secrets.config" "scan-secrets.replacements"
  "scan-secrets.baseline" "package-lock.json" "pnpm-lock.yaml" "go.sum")
EXCLUDE_EXTS=("png" "jpg" "jpeg" "gif" "ico" "svg" "woff" "woff2" "ttf" "eot" "pdf" "zip" "tar" "gz" "bin" "exe" "dll" "so" "dylib" "class" "jar")

TOTAL_FILES_SCANNED=0
TOTAL_FINDINGS=0
declare -A FINDINGS_BY_CATEGORY=()
declare -A FINDINGS_BY_SEVERITY=()
declare -a FINDINGS_JSON=()

is_excluded_path() {
  local path="$1"
  for d in "${EXCLUDE_DIRS[@]}"; do
    [[ "$path" == *"/$d/"* || "$path" == *"/$d" || "$path" == "$d" ]] && return 0
  done
  local base; base="$(basename "$path")"
  for f in "${EXCLUDE_FILES[@]}"; do [[ "$base" == $f ]] && return 0; done
  local ext="${base##*.}"
  for e in "${EXCLUDE_EXTS[@]}"; do [[ "${ext,,}" == "$e" ]] && return 0; done
  return 1
}

declare -A BASELINE=()
if [[ -n "$BASELINE_FILE" && -f "$BASELINE_FILE" ]]; then
  while IFS= read -r bl; do
    [[ -z "$bl" || "$bl" =~ ^# ]] && continue
    BASELINE["$bl"]=1
  done < "$BASELINE_FILE"
fi

is_baselined() { [[ -n "${BASELINE[$1]:-}" ]] && return 0 || return 1; }

record_finding() {
  local rel="$1" ln="$2" cat="$3" sev="$4" desc="$5" content="$6"
  TOTAL_FINDINGS=$((TOTAL_FINDINGS + 1))
  FINDINGS_BY_CATEGORY[$cat]=$((${FINDINGS_BY_CATEGORY[$cat]:-0} + 1))
  FINDINGS_BY_SEVERITY[$sev]=$((${FINDINGS_BY_SEVERITY[$sev]:-0} + 1))
  local safe="${content:0:200}"
  safe=$(echo "$safe" | sed 's/"/\\"/g' | tr '\n' ' ')
  FINDINGS_JSON+=("{\"file\":\"$rel\",\"line\":$ln,\"category\":\"$cat\",\"severity\":\"$sev\",\"description\":\"$desc\",\"match\":\"$safe\"}")
  if [[ "$FORMAT" == "text" ]]; then
    local color="$RED"; [[ "$sev" == "WARN" ]] && color="$YELLOW"; [[ "$sev" == "INFO" ]] && color="$BLUE"
    echo "${color}[$sev]${RESET} ${BOLD}$rel:$ln${RESET}  ${cat} — ${desc}"
    [[ "$ln" != "0" ]] && echo "    $(echo "$content" | head -c 200)"
  fi
}

scan_file() {
  local file="$1" rel="${file#$REPO_ROOT/}" base; base="$(basename "$file")"
  for i in "${!RULES_PATTERN[@]}"; do
    local is_fn="${RULES_IS_FILENAME[$i]}" pat="${RULES_PATTERN[$i]}" cat="${RULES_CATEGORY[$i]}"
    local sev="${RULES_SEVERITY[$i]}" desc="${RULES_DESC[$i]}"
    if [[ "$is_fn" == "1" ]]; then
      if echo "$base" | grep -qiE -e "$pat" 2>/dev/null; then
        local key="$rel:0:$cat"; is_baselined "$key" && continue
        record_finding "$rel" 0 "$cat" "$sev" "$desc" "filename: $base"
      fi
    else
      local matches; matches=$(grep -niE -e "$pat" "$file" 2>/dev/null || true)
      [[ -z "$matches" ]] && continue
      while IFS= read -r match_line; do
        [[ -z "$match_line" ]] && continue
        local ln="${match_line%%:*}" content="${match_line#*:}"
        local whitelisted=0
        for wl in "${WHITELIST_PATTERNS[@]}"; do
          if echo "$content" | grep -qiE -e "$wl" 2>/dev/null; then whitelisted=1; break; fi
        done
        [[ $whitelisted -eq 1 ]] && continue
        local key="$rel:$ln:$cat"; is_baselined "$key" && continue
        record_finding "$rel" "$ln" "$cat" "$sev" "$desc" "$content"
      done <<<"$matches"
    fi
  done
}

scan_working_tree() {
  if [[ $TRACKED_ONLY -eq 1 ]] && git rev-parse --git-dir >/dev/null 2>&1; then
    while IFS= read -r -d '' file; do
      is_excluded_path "$file" && continue
      TOTAL_FILES_SCANNED=$((TOTAL_FILES_SCANNED + 1))
      scan_file "$file"
    done < <(git ls-files -z -- "${PATHS[@]}" 2>/dev/null | while IFS= read -r -d '' f; do
      [[ -f "$REPO_ROOT/$f" ]] && printf '%s\0' "$REPO_ROOT/$f"
    done)
  else
    while IFS= read -r -d '' file; do
      is_excluded_path "$file" && continue
      TOTAL_FILES_SCANNED=$((TOTAL_FILES_SCANNED + 1))
      scan_file "$file"
    done < <(find "${PATHS[@]}" -type f -print0 2>/dev/null)
  fi
}

echo "${BOLD}🔍 SI-LLM-Gateway Secret Scanner${RESET}"
echo "   Mode:    $MODE"
echo "   Paths:   ${PATHS[*]}"
echo "   Format:  $FORMAT"
echo "   Tracked: $([[ $TRACKED_ONLY -eq 1 ]] && echo "yes" || echo "no")"
echo "   Rules:   ${#RULES_PATTERN[@]} patterns"
echo ""
scan_working_tree

echo ""
echo "${BOLD}─ Scan Summary ─${RESET}"
echo "  Files scanned: $TOTAL_FILES_SCANNED"
echo "  Total findings: $TOTAL_FINDINGS"
if [[ ${#FINDINGS_BY_CATEGORY[@]} -gt 0 ]]; then
  echo ""
  echo "  By category:"
  for cat in "${!FINDINGS_BY_CATEGORY[@]}"; do
    printf "    %-22s %d\n" "$cat" "${FINDINGS_BY_CATEGORY[$cat]}"
  done
fi
if [[ ${#FINDINGS_BY_SEVERITY[@]} -gt 0 ]]; then
  echo ""
  echo "  By severity:"
  for sev in BLOCK WARN INFO; do
    [[ -n "${FINDINGS_BY_SEVERITY[$sev]:-}" ]] && printf "    %-22s %d\n" "$sev" "${FINDINGS_BY_SEVERITY[$sev]}"
  done
fi
if [[ "$FORMAT" == "json" ]]; then
  echo ""
  echo "JSON_RESULTS_START"
  printf '%s\n' "${FINDINGS_JSON[@]}"
  echo "JSON_RESULTS_END"
fi

DECISION="clean"
if [[ ${FINDINGS_BY_SEVERITY[BLOCK]:-0} -gt 0 ]]; then DECISION="block"
elif [[ $TOTAL_FINDINGS -gt 0 ]]; then
  case "$MODE" in strict) DECISION="block" ;; normal|permissive) DECISION="warn" ;; esac
fi
case "$DECISION" in
  block) echo ""; echo "${RED}${BOLD}❌ PUSH BLOCKED${RESET}"; exit 1 ;;
  warn)  echo ""; echo "${YELLOW}${BOLD}⚠️  WARNING${RESET}"; exit 0 ;;
  clean) echo ""; echo "${GREEN}${BOLD}✅ CLEAN${RESET}"; exit 0 ;;
esac
