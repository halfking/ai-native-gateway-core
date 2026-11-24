#!/usr/bin/env bash
# gate-evidence.sh —— 部署闸门【被中断时的证据留存】
#
# ── 为什么需要它（★ 2026-11-24 实测到的机制缺陷）────────────────────────
#
#	闸门把读数写在 workdir 里（★ current.txt / base.txt / *.part）⇒
#	★ 而 workdir 由 EXIT trap 的 cleanup() **无条件删除** ⇒
#	★★ 被 SIGTERM / SIGKILL 掐断的那一轮，★ **它自己产出的读数被一起删掉**
#	★★ ⇒ 「基线红清单」永远是残缺的 ⇒ ⇒ 差集归因**不能做**
#	★★ ⇒ 只能另去隔离 worktree 上定向复跑（★ 本会话为 6 条红做过一次、★ 8.001s）。
#
#	★ 这不是「读数不准」、★ 是「**量具在被掐断时毁掉自己的证据**」⇒
#	★ 属于**纯量具缺陷**（★ 不动「比哪些包」那个口径）★。
#
# ── 本脚本只做一件事 ────────────────────────────────────────────────────
#
#	把 workdir 里**已经产出**的读数拷到一个【稳定的目录】，★ 并在 README 里
#	写清「基线可能不完整 ⇒ 差集只能在【基线已完成的包】上做」。
#
#	★ **不判断红绿、不改任何判据** ⇒ ★ 它只是【把已经算出来的读数留下来】。
#
# ── 用法 ────────────────────────────────────────────────────────────────
#
#	source scripts/lib/gate-evidence.sh
#	gate_evidence_preserve <workdir> <evidence_root> <label>
#	  ⇒ 打印留存目录（★ 失败时打印空行、★ 不影响调用方的退出码）

# gate_evidence_preserve —— 把 workdir 里的读数留存到稳定目录。
#
# ★ 参数：
#   $1 = workdir（★ 闸门的临时工作目录）
#   $2 = evidence_root（★ 稳定证据根目录）
#   $3 = label（★ 本次留存的标签、★ 默认 gate）
#
# ★ 返回：★ 打印留存目录路径（★ 任何失败都**不**让调用方失败 ⇒ ★ 证据留存
#   永远不能反过来把闸门搞挂）。
gate_evidence_preserve() {
  local workdir="${1:-}" root="${2:-}" label="${3:-gate}"
  local stamp dest f

  [[ -n "$workdir" && -d "$workdir" ]] || return 0
  [[ -n "$root" ]] || return 0

  stamp="$(date +%Y%m%d-%H%M%S)"
  dest="$root/${label}-${stamp}-$$"
  mkdir -p "$dest" 2>/dev/null || return 0

  for f in "$workdir"/*.txt "$workdir"/*.part; do
    [[ -f "$f" ]] || continue
    cp -f "$f" "$dest/" 2>/dev/null || true
  done

  {
    echo "# 闸门未得出结论就被中断（★ SIGTERM / SIGKILL）⇒ 这里是当时已产出的读数"
    echo "# 生成时间（UTC）：$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# 标签：$label"
    echo "# 证据目录：$dest"
    echo "#"
    echo "# 文件含义："
    echo "#   · current.txt = 当前树读数"
    echo "#   · base.txt    = 基线树读数（★ 可能【不完整】）"
    echo "#   · *.part      = 分箱中的中间输出（★ tee 流式写入）"
    echo "#"
    echo "# ★★ 使用须知（★ 别把它当成完整读数）：★★"
    echo "#   ★ 基线可能只跑完了部分包 ⇒ ★ 「基线红清单」是**残缺的** ⇒"
    echo "#   ★ ⇒ **差集只能在【基线已完成的包】上做**（★ 那部分可靠）⇒"
    echo "#   ★ ⇒ 其余包必须另做定向复跑（★ 隔离 worktree 上只跑那几条）★。"
  } > "$dest/README.md" 2>/dev/null || true

  printf '%s\n' "$dest"
  return 0
}
