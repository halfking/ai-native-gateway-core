#!/bin/bash
# scripts/e2e-identity-session.sh
# 用途: E2E 验证 identity + session + authentication 领域集成
# 调用方式: ./scripts/e2e-identity-session.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$ROOT_DIR"

echo "=== [1/5] Build domains ==="
go build ./domains/identity/... ./domains/session/... ./domains/authentication/... ./domains/integration/...

echo "=== [2/5] Unit-test with coverage (fresh) ==="
go test ./domains/identity/... -count=1 -coverprofile=cover-identity.out
go test ./domains/session/... -count=1 -coverprofile=cover-session.out
go test ./domains/authentication/... -count=1 -coverprofile=cover-auth.out
go test ./domains/integration/... -count=1 -coverprofile=cover-integration.out

echo "=== [3/5] Coverage summary ==="
for f in cover-identity cover-session cover-auth cover-integration; do
    echo -n "$f: "
    go tool cover -func="${f}.out" | tail -1
done

echo "=== [4/5] go vet ==="
go vet ./domains/identity/... ./domains/session/... ./domains/authentication/... ./domains/integration/...

echo "=== [5/5] Circular-dep check ==="
./scripts/check-cycles.sh

# 清理
rm -f cover-identity.out cover-session.out cover-auth.out cover-integration.out

echo "=== Done. All checks passed. ==="
