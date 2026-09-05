#!/usr/bin/env bash
# SOURCE_OF_TRUTH: ~/workspace/ai-native-tools/llm-gateway/ai-native-maintain/scripts/lifecycle/service-runner.sh
# SYNC_POLICY: 修改本文件时同步改 maintain 对应位置。
# USAGE: 完全 binary-name-agnostic；service-runner.sh --config /etc/.../gateway.env --binary /opt/llm-gateway/current/bin/gateway。
#        config 文件每行 KEY=VALUE 注入到进程环境。

set -euo pipefail
config_file=""
binary=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) config_file=${2:-}; shift 2 ;;
    --binary) binary=${2:-}; shift 2 ;;
    *) printf 'service-runner: unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ -n "$config_file" && -r "$config_file" ]] || { printf 'service-runner: config file is required and must be readable\n' >&2; exit 1; }
[[ -n "$binary" && -x "$binary" ]] || { printf 'service-runner: executable binary not found\n' >&2; exit 1; }

while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in ''|'#'*) continue ;; esac
  [[ "$line" == *=* ]] || { printf 'service-runner: malformed config line\n' >&2; exit 1; }
  key=${line%%=*}
  value=${line#*=}
  [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || { printf 'service-runner: unsafe config key: %s\n' "$key" >&2; exit 1; }
  if [[ "$value" == \"*\" ]]; then
    value=${value#\"}
    value=${value%\"}
  fi
  export "$key=$value"
done < "$config_file"

exec "$binary"
