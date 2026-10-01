#!/usr/bin/env bash
# 推导「仅由 integration build tag 引入测试文件」的包列表，每行一个 ./相对路径。
#
# 为什么不 grep 源码：harness 的注释记着一个已踩过的坑——`//go:build !nintegration`
# 也含 "integration" 子串，grep 会把一个**明确排除**集成测试的文件算成集成测试。
# 实测过这条：grep 推导会多出 internal/collector，而门禁对它的判词是
# 「没有任何仅由 integration build tag 引入的测试文件」。
#
# go list 已经求值过构建约束，所以带 tag 与不带 tag 两次列出的差集，恰好是
# 「确实只因 tag 才存在的测试文件」。这与 run-integration-gate.sh 内部的
# GATE_ITEST_COUNT 用的是同一套判据。
#
# 注意：本脚本在**仓库根**跑 go list ./...，而 installer/ 是独立 module，
# 不会被主 module 的 ./... 覆盖（实测 go list 报 "main module does not
# contain package"）。独立 module 的包不进本列表。
#
# 用法：./scripts/audit/derive-gate-packages.sh
set -euo pipefail

# scripts/audit -> 仓库根
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

files() {
  go list "$@" -f '{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}' ./... 2>/dev/null | sort
}

comm -13 <(files) <(files -tags=integration) \
  | xargs -n1 dirname \
  | sort -u \
  | sed "s#^${REPO_ROOT}#.#"
