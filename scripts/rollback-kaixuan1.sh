#!/bin/bash
# scripts/rollback-kaixuan1.sh — kaixuan-1 回滚（仓库级便捷包装）
#
# 调用 ~/.agents/skills/deploy-kaixuan1/scripts/rollback.sh

set -euo pipefail

exec bash ~/.agents/skills/deploy-kaixuan1/scripts/rollback.sh "$@"
