#!/usr/bin/env bash
# ledger-checksum-plaintext-scan.sh — 检出把 sha256 明文写进迁移台账的脚本。
#
# 用法: ledger-checksum-plaintext-scan.sh [目录或文件 ...]
#       默认扫描本仓 scripts/。stdout 最后一行输出命中行数（供调用方断言）。
#       命中 > 0 时以非零码退出，违规明细写 stderr。
#
# 依据：输出脱敏管道会把明文 sha256 十六进制串替换成 [REDACTED:*] 替身，
# 送进 psql 后污染的是生产台账 llm_gateway_migration_checksums。
# 合规写法是 convert_from(decode('<hex>','hex'),'UTF8')：那里 64 位十六
# 进制串是**十六进制编码的十六进制**，即 128 个十六进制字符，不该被命中。
set -uo pipefail

targets=("$@")
if [[ ${#targets[@]} -eq 0 ]]; then
  targets=("$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)")
fi

# 只在「台账表名」与「恰好 64 位十六进制」同行时命中。64 位用 -E 的
# 前后否定查找界定，避免命中 128 位的双重编码字面量。
pattern='llm_gateway_migration_checksums'
hex_re='(^|[^0-9a-f])[0-9a-f]{64}([^0-9a-f]|$)'

# 不用 while read + 进程替换（会与调用方 stdin 争用），改成命令替换赋值。
matches="$(grep -rnE "$pattern" "${targets[@]}" 2>/dev/null | grep -E "$hex_re" || true)"
hits=0
if [[ -n "$matches" ]]; then
  hits="$(printf '%s\n' "$matches" | grep -c '')"
  printf '%s\n' "$matches" | while IFS= read -r line; do
    printf 'PLAINTEXT_LEDGER_CHECKSUM: %s\n' "$line" >&2
  done
fi

printf '%s\n' "$hits"
[[ $hits -eq 0 ]]
