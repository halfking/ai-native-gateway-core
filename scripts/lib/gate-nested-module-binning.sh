#!/usr/bin/env bash
# =====================================================================
# scripts/lib/gate-nested-module-binning.sh — 部署前测试闸门的「按模块根分箱」
#
# ★ 为什么必须独立成库（而不是在闸门脚本里内联）：这段判据要被**实跑**测到
#   ⇒ tests/deploy_test_gate_contract_test.sh（AC-7）直接 source 本库、
#   在临时目录里造嵌套模块、断言分箱结果。
#
# gate_bin_packages_by_module <repo_root> <packages...>
#   把「该跑的包」按所属 Go 模块分箱，结果放在三个全局变量里：
#     GATE_MAIN_PACKAGES   （数组）主模块的包，原样保留
#     GATE_NESTED_MODULES  （数组）嵌套模块根，去重、按首次出现排序，如 installer
#     GATE_NESTED_PKGS     （关联数组）模块根 ⇒ 空格分隔的**模块内相对**包列表
#
# ★ 背景（2026-10-30 部署实测缺陷）：installer/ 自带 go.mod ⇒ 是嵌套模块，从主模块根
#   `go test ./installer/...` 必然 setup failed（"main module does not contain package"）
#   ⇒ 嵌套模块的包必须在**它自己的模块根**下跑；覆盖断言的 `go list` 同根同标志。
#
# ★ 2026-11-26 订正（L123 轮部署实测）：被选中的包**就是嵌套模块根目录本身**时
#   （例：`./plugin-runtime/config` —— 上游直接改了模块根下的文件），模块内相对
#   路径是空的 ⇒ 必须记成 `./`。此前记成 `./plugin-runtime/config` ⇒ 到模块根下
#   再列一次 ⇒ 路径重复一层（`…/plugin-runtime/config/plugin-runtime/config`）
#   ⇒ `go list` 取不到包清单 ⇒ 闸门「未得出结论」而中止 ⇒ 干净树部署被挡。
#   子目录的包（`./installer/internal/dbinit`）一直是对的，所以此前没暴露。
#
# 需要 bash ≥ 4.2（关联数组 + declare -gA）。
# =====================================================================

# shellcheck shell=bash

gate_bin_packages_by_module() {
  local repo_root="$1"; shift

  GATE_MAIN_PACKAGES=()
  GATE_NESTED_MODULES=()
  unset GATE_NESTED_PKGS
  declare -gA GATE_NESTED_PKGS=()

  local p dir modroot
  for p in "$@"; do
    dir="${p#./}"; dir="${dir%/}"
    modroot="$dir"
    # 往上找最近的、自带 go.mod 的祖先目录。
    while [[ -n "$modroot" && "$modroot" != "." && ! -f "$repo_root/$modroot/go.mod" ]]; do
      modroot="$(dirname "$modroot")"
    done
    if [[ -z "$modroot" || "$modroot" == "." || ! -f "$repo_root/$modroot/go.mod" ]]; then
      GATE_MAIN_PACKAGES+=("$p")
      continue
    fi
    if [[ -z "${GATE_NESTED_PKGS[$modroot]+x}" ]]; then
      GATE_NESTED_MODULES+=("$modroot")
    fi
    if [[ "$dir" == "$modroot" ]]; then
      # 包就是模块根目录本身 ⇒ 模块内相对路径是空的 ⇒ `./`。
      GATE_NESTED_PKGS[$modroot]+=" ./"
    else
      GATE_NESTED_PKGS[$modroot]+=" ./${dir#"$modroot"/}"
    fi
  done
}
