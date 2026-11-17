#!/usr/bin/env bash
# go-toolchain.sh —— 【按 go.mod 的版本要求挑 Go 工具链】的 shell 侧实现
#
# ★ 为什么需要它（2026-11-17 F3 残留地平线根修轮实测）：
#
#   scripts/deploy-test-gate.sh 原来按「**第一个存在的 go**」挑工具链：
#       for c in "$(command -v go)" "$HOME/sdk/go/bin/go" /usr/local/go/bin/go /opt/homebrew/bin/go
#   ⇒ 本机命中 /Users/<u>/homebrew/bin/go = **go1.27.3**，而 go.mod 要求
#     **go >= 1.27.4** ⇒ 原生路径被 go.mod **直接拒绝**
#     （"go: go.mod requires go >= 1.27.4 (running go 1.27.3; GOTOOLCHAIN=local)"）
#   ⇒ 于是落 **docker 兜底** ⇒ 而兜底**没设 CGO_ENABLED=1** ⇒ 8 条
#     `TestLiteRequestLogSink_*` 全红（「当前二进制以 CGO_ENABLED=0 构建，
#     SQLite 驱动不可用」）⇒ **一次部署卡在门上**。
#
#   ⇒ 本库把「挑工具链」的判据从**存在性**改成**版本满足性**：
#     · 读 go.mod 的 `go X.Y.Z` 指令作为**要求**；
#     · 逐个候选取 `go version`，**第一个满足要求**的即采用；
#     · 都不满足 ⇒ **返回空**（交由调用方走 docker 兜底）⇒ 而不是挑一个
#       注定被 go.mod 拒绝的旧版 go。
#
# ★ 用法：
#
#   . scripts/lib/go-toolchain.sh
#   llmgw_go_pick ./go.mod "$(command -v go)" "$HOME/sdk/go/bin/go" ...
#     ⇒ 打印选中的 go 路径（**空** = 没有满足要求的候选）
#   llmgw_go_required_version ./go.mod      ⇒ 打印 go.mod 要求的版本（缺省 1.0）
#   llmgw_go_toolchain_version /path/to/go  ⇒ 打印该 go 的版本（取不到则空）
#   llmgw_go_version_ge 1.27.4 1.27.3       ⇒ a >= b 时返回 0
#
# ★ 独立可测：scripts/ursmcheck/deploy_test_gate_toolchain_gate_test.go 用
#   **假 go 二进制**（打印不同版本号）实跑本库的挑选逻辑 ⇒ 钉住「按版本挑」。

# llmgw_go_required_version <gomod 路径>
#   ⇒ 打印 go.mod 的 `go X.Y.Z` 指令（缺省 1.0）
llmgw_go_required_version() {
  local gomod="${1:-}" v=""
  if [[ -n "$gomod" && -f "$gomod" ]]; then
    v="$(awk '$1=="go" {print $2; exit}' "$gomod" 2>/dev/null || true)"
  fi
  [[ "$v" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || v="1.0"
  printf '%s' "$v"
}

# llmgw_go_toolchain_version <go 二进制路径>
#   ⇒ 打印该 go 的版本号（"go1.27.4" ⇒ "1.27.4"）；取不到 ⇒ 空
llmgw_go_toolchain_version() {
  local bin="${1:-}" v=""
  [[ -n "$bin" && -x "$bin" ]] || return 0
  v="$(GOTOOLCHAIN=local "$bin" version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
  [[ "$v" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || v=""
  printf '%s' "$v"
}

# llmgw_go_version_ge <a> <b>
#   ⇒ a >= b 时返回 0（逐段数值比较；缺失段当 0；非数字字符剔除）
llmgw_go_version_ge() {
  local a="${1:-0}" b="${2:-0}" i x y
  local A B
  IFS='.' read -r -a A <<<"$a"
  IFS='.' read -r -a B <<<"$b"
  for i in 0 1 2; do
    x="${A[$i]:-0}"; y="${B[$i]:-0}"
    x="${x//[^0-9]/}"; y="${y//[^0-9]/}"
    [[ -z "$x" ]] && x=0
    [[ -z "$y" ]] && y=0
    if (( x > y )); then return 0; fi
    if (( x < y )); then return 1; fi
  done
  return 0
}

# llmgw_go_pick <gomod 路径> <候选 go 路径...>
#   ⇒ 打印**第一个满足 go.mod 要求**的候选路径；都不满足 ⇒ 打印空
llmgw_go_pick() {
  local gomod="${1:-}"; shift || true
  local req c v
  req="$(llmgw_go_required_version "$gomod")"
  for c in "$@"; do
    [[ -n "$c" && -x "$c" ]] || continue
    v="$(llmgw_go_toolchain_version "$c")"
    [[ -z "$v" ]] && continue
    if llmgw_go_version_ge "$v" "$req"; then
      printf '%s' "$c"
      return 0
    fi
  done
  return 0
}

# llmgw_go_pick_detail <gomod 路径> <候选 go 路径...>
#   ⇒ 打印两行：第 1 行 = 选中的路径（可能空）；第 2 行 = 诊断
#     "required=<req> candidates=<逐个候选的版本或 none>"
llmgw_go_pick_detail() {
  local gomod="${1:-}"; shift || true
  local req c v picked="" detail=""
  req="$(llmgw_go_required_version "$gomod")"
  for c in "$@"; do
    [[ -n "$c" && -x "$c" ]] || continue
    v="$(llmgw_go_toolchain_version "$c")"
    [[ -z "$v" ]] && v="none"
    detail="${detail}${c}=${v} "
    if [[ -z "$picked" && "$v" != "none" ]] && llmgw_go_version_ge "$v" "$req"; then
      picked="$c"
    fi
  done
  printf '%s\n' "$picked"
  printf 'required=%s %s\n' "$req" "${detail:-no-candidate}"
}

# ★ 直接执行时：打印诊断（便于人工核对）。
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  llmgw_go_pick_detail "${1:-./go.mod}" "${@:2}"
fi
