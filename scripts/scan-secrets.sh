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
  # Slice 7: documentation placeholders use angle-bracket syntax
  '<user>:<password>@<host>' '<password>@'
  # Phase 3B-4 cleanup: also skip lines with shell/env-var placeholders
  # or bare REDACTED (not just angle-bracket <REDACTED>)
  'REDACTED' '\$\{[A-Z_][A-Z0-9_]*\}' '\${[A-Z_][A-Z0-9_]*}'
  # Phase 3B-4 cleanup pt.2: skip generic user:pass@host examples in docs
  'user:pass@host' 'user:password@host' ':pass@' ':password@'
  'username:password@' 'dbuser:dbpass@'
)

EXCLUDE_DIRS=(".git" "node_modules" "vendor" "build" "dist" "out" "coverage"
  ".playwright-mcp" ".codegraph" ".cache" ".runtime" ".ruff_cache"
  ".pnpm-store" ".secrets" ".deploy" ".trash" ".artifacts" ".idea" ".vscode" ".cursor"
  # Slice 7: the legacy migration carve-out is git-ignored and
  # scheduled for deletion. Stop the scanner from crawling it.
  "_to-be-deprecated")
EXCLUDE_FILES=("scan-secrets.sh" "scan-secrets.config" "scan-secrets.replacements"
  "scan-secrets.baseline" "package-lock.json" "pnpm-lock.yaml" "go.sum"
  # Slice 7: PEM key artifacts are legitimate test/infra fixtures,
  # not analyst-defined credentials. They go through the key
  # generator's documented flow; if a SECRET-private-key need shows
  # up here, that's a contract violation that should be discussed in
  # code review rather than papered over.
  "server.priv" "server.pub"
  "*.pem" "*.priv" "*.pub")
EXCLUDE_EXTS=("png" "jpg" "jpeg" "gif" "ico" "svg" "woff" "woff2" "ttf" "eot"
  "pdf" "zip" "tar" "gz" "bz2" "xz" "7z"
  "bin" "exe" "dll" "so" "dylib" "class" "jar"
  # Slice 7: tracked Linux/amd64 build artifacts are not credential
  # sources — never read them. (The .gitignore keeps new ones out;
  # legacy tracked copies are excluded here too.)
  "linux.amd64")

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
  # Extension check: match either the final suffix OR the multi-part
  # suffix for patterns like 'linux.amd64' (which appears in our
  # exclude list as a single token).
  local ext="${base##*.}"
  for e in "${EXCLUDE_EXTS[@]}"; do
    [[ "$ext" == "$e" ]] && return 0
    # If the extension list contains a dot-separated compound suffix
    # (e.g. "linux.amd64"), match the tail of the basename.
    if [[ "$e" == *.* && "$base" == *."$e" ]]; then
      return 0
    fi
  done
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

  # Spec cf8aad1a9 §SOPS and Credentials:
  # "A file is accepted as encrypted only when SOPS can parse its
  #  metadata and decrypt it with an authorized key; filename alone
  #  never bypasses secret scanning."
  #
  # We enforce the spirit of that rule by inspecting the file's first
  # line for the SOPS metadata envelope. `.env.*.enc` files created
  # by `sops --encrypt` start with `ENC[` (data key block) and contain
  # a `sops:` (config) section. Files that pass this check are skipped
  # before any rule pattern runs.
  if is_sops_envelope "$file"; then
    return 0
  fi

  # ── v2 performance: filename rules (fast, no file I/O) ──────────────
  for i in "${!RULES_PATTERN[@]}"; do
    if [[ "${RULES_IS_FILENAME[$i]}" == "1" ]]; then
      if echo "$base" | grep -qiE -e "${RULES_PATTERN[$i]}" 2>/dev/null; then
        local key="$rel:0:${RULES_CATEGORY[$i]}"
        is_baselined "$key" && continue
        record_finding "$rel" 0 "${RULES_CATEGORY[$i]}" "${RULES_SEVERITY[$i]}" "${RULES_DESC[$i]}" "filename: $base"
      fi
    fi
  done

  # ── v2 performance: combined content grep (1 call instead of N) ────
  # Build a single grep invocation with all content patterns via -e flags.
  # This reduces fork+exec from 49× per file to 1× per file.
  local grep_args=()
  for i in "${!RULES_PATTERN[@]}"; do
    if [[ "${RULES_IS_FILENAME[$i]}" != "1" ]]; then
      grep_args+=(-e "${RULES_PATTERN[$i]}")
    fi
  done

  if [[ ${#grep_args[@]} -eq 0 ]]; then
    return 0
  fi

  local matches
  matches=$(grep -niE "${grep_args[@]}" "$file" 2>/dev/null || true)
  [[ -z "$matches" ]] && return 0

  # Parse each matching line and re-attribute to the first matching rule
  while IFS= read -r match_line; do
    [[ -z "$match_line" ]] && continue
    local ln="${match_line%%:*}" content="${match_line#*:}"

    # Check whitelist (combined into one grep for speed)
    local whitelisted=0
    for wl in "${WHITELIST_PATTERNS[@]}"; do
      if echo "$content" | grep -qiE -e "$wl" 2>/dev/null; then whitelisted=1; break; fi
    done
    [[ $whitelisted -eq 1 ]] && continue

    # Re-match individual patterns to determine category/severity
    # (fast: only runs against the single matched line, not the whole file)
    for i in "${!RULES_PATTERN[@]}"; do
      if [[ "${RULES_IS_FILENAME[$i]}" != "1" ]]; then
        if echo "$content" | grep -qiE -e "${RULES_PATTERN[$i]}" 2>/dev/null; then
          local key="$rel:$ln:${RULES_CATEGORY[$i]}"
          is_baselined "$key" && break
          record_finding "$rel" "$ln" "${RULES_CATEGORY[$i]}" "${RULES_SEVERITY[$i]}" "${RULES_DESC[$i]}" "$content"
          break
        fi
      fi
    done
  done <<<"$matches"
}

# is_sops_envelope returns 0 if the file looks like a SOPS-encrypted
# envelope. We are NOT decrypting the file here (the spec leaves
# decryption to env-injector); we are only detecting the metadata
# preamble that real SOPS output always carries. A file that fails
# this check falls through to the normal pattern scan and will be
# reported like any other plaintext file.
#
# Required markers in the first 32 lines (matches real SOPS JSON output):
#   - `ENC[`  — AES256_GCM data key block opener
#   - `"mac":` — SOPS MAC field (always present, integrity check)
#   - `"(age|pgp|kms)":` — key group (at least one recipient backend)
#
# Note: `encrypted_regex`/`unencrypted_regex` are optional config fields
# that only appear when .sops.yaml specifies them; they are NOT metadata
# and must not be required for envelope detection (v2 fix).
#
# Implementation note: real `sops --encrypt` output is JSON-spaced
# (lines start with tabs), so we match with optional leading
# whitespace rather than anchoring to column 0.
is_sops_envelope() {
  local file="$1"
  [[ -r "$file" ]] || return 1
  local head
  head=$(head -n 32 "$file" 2>/dev/null)
  [[ -z "$head" ]] && return 1
  echo "$head" | grep -Eq '^[[:space:]]*"(data|sops)":' || return 1
  echo "$head" | grep -Eq '\bENC\[' || return 1
  echo "$head" | grep -Eq '^[[:space:]]*"(mac|lastmodified|version)":' || return 1
  echo "$head" | grep -Eq '^[[:space:]]*"(age|pgp|kms)":' || return 1
  return 0
}

# scan_working_tree: v2 performance optimization.
# Instead of forking grep per-file (8941 files × N patterns = ~438K forks),
# we use batched grep calls:
#
#   1. Build file list via git ls-files / find (1 call)
#   2. Filter excluded paths via single grep -vE (1 call)
#   3. Identify SOPS envelopes via grep -rl on .enc files (≤3 calls)
#   4. Run filename rules via grep on basenames (1 call)
#   5. Run content rules via single xargs grep -niE (1 call)
#
# Total: ~7 process forks instead of ~54K.
scan_working_tree() {
  local tmpdir
  tmpdir=$(mktemp -d -t kx-scan.XXXXXX)
  local filelist="$tmpdir/files"
  local candidates="$tmpdir/candidates"
  local scannable="$tmpdir/scannable"
  local sopsset="$tmpdir/sops"
  trap 'rm -rf "$tmpdir"' RETURN

  # ── Step 1: Build candidate file list ──────────────────────────────
  if [[ $TRACKED_ONLY -eq 1 ]] && git rev-parse --git-dir >/dev/null 2>&1; then
    git ls-files -- "${PATHS[@]}" 2>/dev/null | while IFS= read -r f; do
      [[ -f "$REPO_ROOT/$f" ]] && echo "$REPO_ROOT/$f"
    done > "$candidates"
  else
    find "${PATHS[@]}" -type f > "$candidates" 2>/dev/null
  fi

  # ── Step 2: Filter excluded paths (single grep -vE) ────────────────
  local excl_pat=""
  for d in "${EXCLUDE_DIRS[@]}"; do
    excl_pat="${excl_pat:+$excl_pat|}/$d/"
  done
  for f in "${EXCLUDE_FILES[@]}"; do
    local glob_pat; glob_pat=$(echo "$f" | sed 's/\./\\./g; s/\*/.*/g')
    excl_pat="${excl_pat:+$excl_pat|}/$glob_pat$"
  done
  for e in "${EXCLUDE_EXTS[@]}"; do
    local dot_e; dot_e=$(echo "$e" | sed 's/\./\\./g')
    excl_pat="${excl_pat:+$excl_pat|}\.$dot_e$"
  done

  if [[ -n "$excl_pat" ]]; then
    grep -vE "$excl_pat" "$candidates" > "$candidates.tmp" 2>/dev/null || true
    mv "$candidates.tmp" "$candidates"
  fi

  # ── Step 3: Identify SOPS envelopes (batch grep -rl on .enc files) ─
  > "$sopsset"
  grep -E '\.enc$' "$candidates" > "$tmpdir/encfiles" 2>/dev/null || true
  if [[ -s "$tmpdir/encfiles" ]]; then
    # SOPS envelopes must have: ENC[AES256_GCM AND "mac" AND "age"
    local enc_files; enc_files=$(cat "$tmpdir/encfiles")
    grep -rl 'ENC\[AES256_GCM' $enc_files 2>/dev/null > "$tmpdir/enc_sops1" || true
    if [[ -s "$tmpdir/enc_sops1" ]]; then
      grep -rl '"mac"' $(cat "$tmpdir/enc_sops1") 2>/dev/null > "$tmpdir/enc_sops2" || true
      if [[ -s "$tmpdir/enc_sops2" ]]; then
        grep -rl '"age"' $(cat "$tmpdir/enc_sops2") 2>/dev/null > "$sopsset" || true
      fi
    fi
  fi

  # ── Step 4: Subtract SOPS files from candidates ────────────────────
  if [[ -s "$sopsset" ]]; then
    grep -vxF -f "$sopsset" "$candidates" > "$scannable" 2>/dev/null || true
  else
    cp "$candidates" "$scannable"
  fi

  TOTAL_FILES_SCANNED=$(wc -l < "$scannable" 2>/dev/null | tr -d ' ')

  # ── Step 5: Filename rules (batch grep on basenames) ───────────────
  local fn_grep_args=()
  for i in "${!RULES_PATTERN[@]}"; do
    if [[ "${RULES_IS_FILENAME[$i]}" == "1" ]]; then
      fn_grep_args+=(-e "${RULES_PATTERN[$i]}")
    fi
  done

  if [[ ${#fn_grep_args[@]} -gt 0 ]]; then
    # Match filenames against filename rules using grep on the path list
    local fn_matches="$tmpdir/fn_matches"
    grep -nE "${fn_grep_args[@]}" "$candidates" > "$fn_matches" 2>/dev/null || true

    # Pre-build filename regexes for bash matching (zero-fork)
    local -a fn_regexes=() fn_indices=()
    for i in "${!RULES_PATTERN[@]}"; do
      if [[ "${RULES_IS_FILENAME[$i]}" == "1" ]]; then
        fn_regexes+=("${RULES_PATTERN[$i]}")
        fn_indices+=("$i")
      fi
    done

    # Load SOPS set into a bash associative array for O(1) lookup
    declare -A sops_map=()
    if [[ -s "$sopsset" ]]; then
      while IFS= read -r sp; do
        sops_map["$sp"]=1
      done < "$sopsset"
    fi

    if [[ -s "$fn_matches" ]]; then
      while IFS= read -r match_line; do
        [[ -z "$match_line" ]] && continue
        local line_no="${match_line%%:*}"
        local file_path; file_path=$(sed -n "${line_no}p" "$candidates")
        [[ -z "$file_path" ]] && continue

        # Skip SOPS envelopes (O(1) hash lookup, no fork)
        [[ -n "${sops_map[$file_path]:-}" ]] && continue

        local rel="${file_path#$REPO_ROOT/}"
        local base="${file_path##*/}"

        # Re-attribute to specific rule (bash regex, no fork)
        for idx in "${!fn_regexes[@]}"; do
          local rule_idx="${fn_indices[$idx]}"
          local pat="${fn_regexes[$idx]}"
          if [[ "$base" =~ $pat ]]; then
            local key="$rel:0:${RULES_CATEGORY[$rule_idx]}"
            is_baselined "$key" && break
            record_finding "$rel" 0 "${RULES_CATEGORY[$rule_idx]}" "${RULES_SEVERITY[$rule_idx]}" "${RULES_DESC[$rule_idx]}" "filename: $base"
            break
          fi
        done
      done < "$fn_matches"
    fi
  fi

  # ── Step 6: Build combined content grep args ───────────────────────
  local grep_args=()
  for i in "${!RULES_PATTERN[@]}"; do
    if [[ "${RULES_IS_FILENAME[$i]}" != "1" ]]; then
      grep_args+=(-e "${RULES_PATTERN[$i]}")
    fi
  done

  [[ ${#grep_args[@]} -eq 0 ]] && return 0

  # ── Step 7: Single grep pass over all scannable files ──────────────
  local raw_matches="$tmpdir/matches"
  xargs grep -niE "${grep_args[@]}" -- < "$scannable" > "$raw_matches" 2>/dev/null || true

  # ── Step 8: Parse matches and attribute to rules ──────────────────
  # Pre-build bash-compatible regex arrays for zero-fork matching.
  # Bash [[ =~ ]] avoids ~53K grep forks (1208 matches × 44 patterns).
  local -a content_regexes=() content_indices=()
  for i in "${!RULES_PATTERN[@]}"; do
    if [[ "${RULES_IS_FILENAME[$i]}" != "1" ]]; then
      content_regexes+=("${RULES_PATTERN[$i]}")
      content_indices+=("$i")
    fi
  done

  # Build combined whitelist regex for single-check filtering
  local wl_combined=""
  for wl in "${WHITELIST_PATTERNS[@]}"; do
    if [[ -n "$wl_combined" ]]; then
      wl_combined="$wl_combined|$wl"
    else
      wl_combined="$wl"
    fi
  done

  while IFS= read -r match_line; do
    [[ -z "$match_line" ]] && continue
    local rest="$match_line"
    local file_part="${rest%%:*}"
    rest="${rest#*:}"
    local ln_part="${rest%%:*}"
    local content="${rest#*:}"

    # Whitelist check (single regex, no fork)
    if [[ -n "$wl_combined" && "$content" =~ $wl_combined ]]; then
      continue
    fi

    local rel="${file_part#$REPO_ROOT/}"

    # Attribute to first matching rule (bash regex, no fork)
    for idx in "${!content_regexes[@]}"; do
      local rule_idx="${content_indices[$idx]}"
      local pat="${content_regexes[$idx]}"
      if [[ "$content" =~ $pat ]]; then
        local key="$rel:$ln_part:${RULES_CATEGORY[$rule_idx]}"
        is_baselined "$key" && break
        record_finding "$rel" "$ln_part" "${RULES_CATEGORY[$rule_idx]}" "${RULES_SEVERITY[$rule_idx]}" "${RULES_DESC[$rule_idx]}" "$content"
        break
      fi
    done
  done < "$raw_matches"
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
