#!/bin/bash
# scripts/e2e-hooks-all.sh
# 用途: E2E 验证所有横切 Hook（Phase 2 全部 6 个领域）
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$ROOT_DIR"

echo "=== [1/4] Build all hooks ==="
go build ./domains/hooks/...

echo "=== [2/4] Run tests with coverage (fresh) ==="
for hook in cache compression security audit observability tools; do
    go test "./domains/hooks/$hook/..." -count=1 -coverprofile="cover-$hook.out" -v
done

echo "=== [3/4] Coverage summary ==="
for hook in cache compression security audit observability tools; do
    echo -n "hooks/$hook: "
    go tool cover -func="cover-$hook.out" | tail -1
done

echo "=== [4/4] Circular-dep check + vet ==="
./scripts/check-cycles.sh
go vet ./domains/hooks/...

# 清理
rm -f cover-*.out

echo "=== Done. All 6 cross-cutting hooks verified. ==="
