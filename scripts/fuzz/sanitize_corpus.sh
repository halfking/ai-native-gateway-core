#!/bin/bash
# fuzz 语料脱敏检查：扫描 testdata/fuzz 语料（或指定崩溃样本文件）中的敏感串。
# 用途：
#   1. make fuzz-corpus-lint —— CI/提交前对已入库语料做门禁；
#   2. fuzz-regress.sh 安装崩溃样本前对其调用，命中则拒绝自动入库。
# 退出码：0 = 干净；1 = 命中敏感串；2 = 用法错误。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CORPUS_DIR="$REPO_ROOT/internal/ir/testdata/fuzz"
# 文件级误报放行清单：每行一个文件名（basename），命中则跳过该文件并提示。
# 仅用于确认无害的历史语料；新增命中默认不放行。
ALLOWLIST_FILE="$REPO_ROOT/scripts/fuzz/allowlist.txt"

# 敏感串模式：真实密钥/令牌/凭据形状。全部为结构特征匹配，误报时允许在
# SENSITIVE_ALLOWLIST 中按文件名:模式 放行（见 README）。
SENSITIVE_PATTERNS=(
  'sk-(proj-)?[A-Za-z0-9_-]{16,}'            # OpenAI 形态密钥
  'Bearer[[:space:]]+[A-Za-z0-9._~+/-]{16,}={0,2}' # HTTP 凭据头
  'AKIA[0-9A-Z]{16}'                          # AWS AccessKey
  'gh[pousr]_[A-Za-z0-9]{30,}'                # GitHub token
  'xox[baprs]-[A-Za-z0-9-]{10,}'              # Slack token
  'AIza[0-9A-Za-z_-]{30,}'                    # Google API key
  'eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}' # JWT
  'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY'     # 私钥块
  '(password|passwd|secret|api[_-]?key|token)"[[:space:]]*:[[:space:]]*"[^"]{8,}"' # 凭据字段带值
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' # 邮箱地址
  '[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}' # IPv4（内网主机名/出口地址）
)

usage() {
  echo "用法: $0 [--all | <文件>...]" >&2
  echo "  --all      扫描 internal/ir/testdata/fuzz 下全部语料（默认）" >&2
  echo "  <文件>     扫描指定崩溃样本/语料文件" >&2
  exit 2
}

is_allowlisted() {
  local base="$1"
  [ -f "$ALLOWLIST_FILE" ] && grep -qxF "$base" "$ALLOWLIST_FILE"
}

scan_file() {
  local file="$1" hit=0 pattern match
  if is_allowlisted "$(basename "$file")"; then
    echo "已按 allowlist 放行: $(basename "$file")"
    return 0
  fi
  for pattern in "${SENSITIVE_PATTERNS[@]}"; do
    # 只报告匹配片段（grep -o），避免把整行语料原文刷进日志造成二次泄漏；
    # -a 让含原始字节的崩溃样本也按文本逐段匹配
    while IFS= read -r match; do
      [ -z "$match" ] && continue
      if [ "$hit" -eq 0 ]; then
        echo "敏感串命中: $file"
        hit=1
      fi
      echo "  模式 /$pattern/ 命中片段: ${match:0:12}...(已截断)"
    done < <(grep -aoE "$pattern" "$file" 2>/dev/null || true)
  done
  return "$hit"
}

targets=()
if [ $# -eq 0 ] || [ "${1:-}" = "--all" ]; then
  [ $# -gt 1 ] && usage
  if [ -d "$CORPUS_DIR" ]; then
    while IFS= read -r f; do targets+=("$f"); done < <(find "$CORPUS_DIR" -type f | sort)
  fi
else
  for f in "$@"; do
    [ -f "$f" ] || { echo "文件不存在: $f" >&2; exit 2; }
    targets+=("$f")
  done
fi

if [ "${#targets[@]}" -eq 0 ]; then
  echo "fuzz 语料目录为空或未指定文件: $CORPUS_DIR"
  exit 0
fi

fail=0
for f in "${targets[@]}"; do
  scan_file "$f" || fail=1
done

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "拒绝通过：语料命中敏感串。处理方式（二选一）："
  echo "  1. 将敏感值替换为等长占位符（保持字节结构以保留复现能力），重新验证崩溃可复现；"
  echo "  2. 若为误报，将文件名（basename）加入 scripts/fuzz/allowlist.txt 放行。"
  exit 1
fi

echo "脱敏检查通过：${#targets[@]} 个文件，未发现敏感串。"
