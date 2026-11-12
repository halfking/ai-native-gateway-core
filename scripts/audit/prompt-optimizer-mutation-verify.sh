#!/usr/bin/env bash
# 变异验证：把已修复的缺陷逐个改回去，确认对应测试会转红。
#
# 为什么必须做（2026-11-11 落地）：绿色测试证明不了"测试有牙"。
# 如果把修复回退后测试仍然全绿，说明这条测试只是一张恒绿假门，
# 比没有门更坏 —— 它会让人以为"这一族已被守住"。
#
# 每条臂：
#   1. 备份原文件
#   2. 用 sed/python 施加变异（复原旧实现的行为）
#   3. 跑对应包的测试
#   4. 断言测试 **必须失败**（转红才算抓住）
#   5. 还原文件
#
# 判定：
#   - 变异后测试仍全绿 ⇒ 「门是哑的」⇒ 失败
#   - 变异后编译失败   ⇒ 不算证据（红的原因不是断言）⇒ 失败
#   - 变异后测试红     ⇒ 抓住 ⇒ 通过

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

PASS=0
FAIL=0
FAILED_ARMS=()

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_pass() { echo -e "${GREEN}✓ PASS${NC} $1"; }
log_fail() { echo -e "${RED}✗ FAIL${NC} $1"; }
log_info() { echo -e "${YELLOW}[INFO]${NC} $1"; }

# run_arm <名称> <包路径> <变异脚本>
#
# 变异脚本通过环境变量 MUTATE_FILE 拿到待改文件路径。
run_arm() {
  local name="$1" pkg="$2" mutate="$3"
  local target_file="$4"

  log_info "变异臂: $name"

  # 备份
  local backup
  backup="$(mktemp)"
  if [[ ! -f "$target_file" ]]; then
    log_fail "$name —— 目标文件不存在: $target_file"
    FAIL=$((FAIL + 1))
    FAILED_ARMS+=("$name")
    rm -f "$backup"
    return
  fi
  cp "$target_file" "$backup"

  # 施加变异
  MUTATE_FILE="$target_file" python3 -c "$mutate" || {
    log_fail "$name —— 变异脚本执行失败"
    FAIL=$((FAIL + 1))
    FAILED_ARMS+=("$name")
    cp "$backup" "$target_file"
    rm -f "$backup"
    return
  }

  # 跑测试
  local out rc
  out="$(go test "$pkg" 2>&1)"
  rc=$?

  # 还原
  cp "$backup" "$target_file"
  rm -f "$backup"

  if [[ $rc -eq 0 ]]; then
    log_fail "$name —— 变异后测试仍全绿 ⇒ 这条测试没有牙"
    FAIL=$((FAIL + 1))
    FAILED_ARMS+=("$name")
  elif echo "$out" | grep -qE '^\s*\[build failed\]|build constraints|cannot use|undefined:'; then
    log_fail "$name —— 变异后是编译失败（红的原因不是断言）⇒ 不算证据"
    echo "$out" | head -5
    FAIL=$((FAIL + 1))
    FAILED_ARMS+=("$name")
  else
    log_pass "$name —— 变异后测试转红 ✓（断言真的生效）"
    PASS=$((PASS + 1))
  fi
}

echo "=========================================="
echo " 提示词优化模块 —— 变异验证"
echo "=========================================="
echo ""

# ── ARM 1：完整性指标退回 len(expected)*0.5（int 截断 ⇒ 恒真） ──────────
run_arm "ARM01 完整性指标退回恒真写法" \
  "./internal/prompt-optimizer/evaluation/..." \
  '
import os, re
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	expLen := float64(len(expected))
	actLen := float64(len(actual))

	if expLen == 0 {
		// 没有期望答案时，只要产出了内容即视为完整。
		if actLen > 0 {
			return 1.0, nil
		}
		return 0.0, nil
	}

	ratio := actLen / expLen
	switch {
	case ratio < 0.5:
		return 0.5, nil
	case ratio >= 0.8:
		return 1.0, nil
	default:
		return 0.75, nil
	}"""
new = """	if len(actual) < len(expected)*0.5 {
		return 0.5, nil
	}
	if len(actual) >= len(expected)*0.8 {
		return 1.0, nil
	}
	return 0.75, nil"""
assert old in s, "ARM01 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/evaluation/evaluator.go"

# ── ARM 2：迭代策略退回"永远无测试用例"（死代码） ──────────────────────
run_arm "ARM02 迭代策略退回硬编码空测试集" \
  "./internal/prompt-optimizer/strategies/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """func (s *IterativeStrategy) getTestCases(strategyCtx optimizer.StrategyContext) []optimizer.TestCase {
	return strategyCtx.TestCases
}"""
new = """func (s *IterativeStrategy) getTestCases(strategyCtx optimizer.StrategyContext) []optimizer.TestCase {
	return []optimizer.TestCase{}
}"""
assert old in s, "ARM02 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/strategies/iterative.go"

# ── ARM 3：意图分析退回"不读正文" ────────────────────────────────────
run_arm "ARM03 意图分析退回硬编码" \
  "./internal/prompt-optimizer/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	if intent.TaskType == "" {
		intent.TaskType = inferTaskType(prompt)
	}
	if intent.Domain == "" {
		intent.Domain = inferDomain(prompt)
	}
	intent.Constraints = extractConstraints(prompt)"""
new = """	// 变异：退回旧行为（不读正文，永远是 general）。"""
assert old in s, "ARM03 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/optimizer.go"

# ── ARM 4：优化器退回"评估器为 nil 直接调用"（会 panic） ────────────────
run_arm "ARM04 评估器 nil 检查被移除" \
  "./internal/prompt-optimizer/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	if o.evaluator == nil {
		return nil, fmt.Errorf("evaluate: evaluator is not configured (use WithEvaluator)")
	}"""
new = """	// 变异：移除 nil 检查。"""
assert old in s, "ARM04 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/optimizer.go"

# ── ARM 5：相对提升退回无除零保护 ──────────────────────────────────────
run_arm "ARM05 提升幅度退回无除零保护" \
  "./internal/prompt-optimizer/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	improvement := 0.0
	if baselineScore != 0 {
		improvement = ((optimizedScore - baselineScore) / baselineScore) * 100
	}"""
new = """	improvement := ((optimizedScore - baselineScore) / baselineScore) * 100"""
assert old in s, "ARM05 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/optimizer.go"

# ── ARM 6：记忆阈值过滤被移除 ──────────────────────────────────────────
run_arm "ARM06 记忆相似度阈值过滤被移除" \
  "./internal/prompt-optimizer/context/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """		if hit.Score < m.cfg.SimilarityThreshold {
			continue
		}"""
new = """		// 变异：移除阈值过滤。"""
assert old in s, "ARM06 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/context/manager.go"

# ── ARM 7：token 预算裁剪被移除 ────────────────────────────────────────
run_arm "ARM07 上下文 token 预算裁剪被移除" \
  "./internal/prompt-optimizer/context/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """		if !w.Add(text) {
			// 预算耗尽：后续更长的片段必然也放不下，直接停止。
			break
		}"""
new = """		_ = w"""
assert old in s, "ARM07 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/context/manager.go"

# ── ARM 8：Harness 章节顺序被破坏 ──────────────────────────────────────
run_arm "ARM08 Harness 章节排序被移除" \
  "./internal/prompt-optimizer/harness/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	for i := 0; i < len(sections); i++ {
		for j := i + 1; j < len(sections); j++ {
			if sections[j].Order < sections[i].Order {
				sections[i], sections[j] = sections[j], sections[i]
			}
		}
	}"""
new = """	// 变异：移除排序。"""
assert old in s, "ARM08 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/harness/injector.go"

# ── ARM 9：Few-shot 质量阈值过滤被移除 ────────────────────────────────
run_arm "ARM09 Few-shot 质量阈值过滤被移除" \
  "./internal/prompt-optimizer/strategies/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	for _, ex := range examples {
		if ex.Quality >= f.config.QualityThreshold {
			filtered = append(filtered, ex)
		}
	}"""
new = """	filtered = append(filtered, examples...)"""
assert old in s, "ARM09 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/strategies/fewshot.go"

# ── ARM 10：结构化策略不再注入任务专属约束 ────────────────────────────
run_arm "ARM10 结构化任务专属约束被移除" \
  "./internal/prompt-optimizer/strategies/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	case "code_generation":
		constraints = append(constraints,
			"- 代码必须遵循最佳实践和编码规范",
			"- 添加必要的注释和文档",
			"- 考虑错误处理和边界情况",
		)"""
new = """	case "code_generation":
		// 变异：移除代码专属约束。"""
assert old in s, "ARM10 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/strategies/structural.go"

# ── ARM 11：结构化策略不再按任务类型生成角色 ──────────────────────────
run_arm "ARM11 结构化角色定义不再随任务变化" \
  "./internal/prompt-optimizer/strategies/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	case "translation":
		role += "专业翻译专家，精通多语言转换，能准确传达原文含义和语境\""""
new = """	case "translation":
		role += "通用助手\""""
assert old in s, "ARM11 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/strategies/structural.go"

# ── ARM 12：Window 预算检查被移除 ─────────────────────────────────────
run_arm "ARM12 Window 预算检查被移除" \
  "./internal/prompt-optimizer/context/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	if w.used+cost > w.maxTokens {
		return false
	}"""
new = """	// 变异：移除预算检查。"""
assert old in s, "ARM12 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/context/window.go"

# ── ARM 13：JSON 字段名退回 Go 默认名 ────────────────────────────────
run_arm "ARM13 JSON 字段名退回 Go 默认名" \
  "./internal/prompt-optimizer/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()
old = """	Original string `json:"original"`"""
new = """	Original string"""
assert old in s, "ARM13 anchor not found"
open(p, "w", encoding="utf-8").write(s.replace(old, new))
' \
  "internal/prompt-optimizer/optimizer.go"

# ── ARM 14：耗时退回纳秒整数（不可读）────────────────────────────────
# 注意：变异必须保证**可编译** —— 编译失败不是证据（harness 明确区分）。
# 因此这里同时改字段类型与赋值处，两处都在同一个文件里。
run_arm "ARM14 耗时退回裸 time.Duration" \
  "./internal/prompt-optimizer/..." \
  '
import os
p = os.environ["MUTATE_FILE"]
s = open(p, encoding="utf-8").read()

old_field = """	Duration Duration `json:"duration"`"""
new_field = """	Duration time.Duration `json:"duration"`"""
old_assign = """Duration:        Duration(time.Since(startTime)),"""
new_assign = """Duration:        time.Since(startTime),"""

assert old_field in s, "ARM14 field anchor not found"
assert old_assign in s, "ARM14 assign anchor not found"
s = s.replace(old_field, new_field).replace(old_assign, new_assign)
open(p, "w", encoding="utf-8").write(s)
' \
  "internal/prompt-optimizer/optimizer.go"

echo ""
echo "=========================================="
if [[ $FAIL -eq 0 ]]; then
  echo -e "${GREEN}全部 $PASS 条变异臂均被测试抓住 ✓${NC}"
  echo "=========================================="
  exit 0
else
  echo -e "${RED}$FAIL 条变异臂未被抓住 ✗${NC}"
  for arm in "${FAILED_ARMS[@]}"; do
    echo "  - $arm"
  done
  echo "=========================================="
  exit 1
fi
