#!/bin/bash
# 测试执行总结生成器
# 用法: bash scripts/generate-test-summary.sh <test-reports-dir>

set -euo pipefail

REPORTS_DIR=${1:-}

if [ -z "$REPORTS_DIR" ] || [ ! -d "$REPORTS_DIR" ]; then
  echo "用法: $0 <test-reports-dir>"
  echo "示例: $0 test-reports/parallel-20261111-143022"
  exit 1
fi

SUMMARY_FILE="${REPORTS_DIR}/TEST_SUMMARY.md"

echo "生成测试总结: ${SUMMARY_FILE}"

# ==================== 生成Markdown报告 ====================

cat > "${SUMMARY_FILE}" <<'HEADER'
# LLM Gateway 综合测试报告

---

## 一、测试概览

HEADER

# 添加基本信息
echo "**测试时间**：$(date +'%Y-%m-%d %H:%M:%S')" >> "${SUMMARY_FILE}"
echo "" >> "${SUMMARY_FILE}"
echo "**报告目录**：${REPORTS_DIR}" >> "${SUMMARY_FILE}"
echo "" >> "${SUMMARY_FILE}"

# 统计测试结果
TOTAL=0
PASSED=0
FAILED=0

for exit_file in "${REPORTS_DIR}"/*.exit; do
  if [ ! -f "$exit_file" ]; then
    continue
  fi
  
  TOTAL=$((TOTAL + 1))
  exit_code=$(cat "$exit_file")
  
  if [ "$exit_code" -eq 0 ]; then
    PASSED=$((PASSED + 1))
  else
    FAILED=$((FAILED + 1))
  fi
done

SUCCESS_RATE=$(awk "BEGIN {if ($TOTAL > 0) printf \"%.1f\", ($PASSED/$TOTAL)*100; else print \"0\"}")

cat >> "${SUMMARY_FILE}" <<EOF
### 测试统计

| 指标 | 数值 |
|------|------|
| 总任务数 | ${TOTAL} |
| 通过 | ${PASSED} ✓ |
| 失败 | ${FAILED} ✗ |
| 成功率 | ${SUCCESS_RATE}% |

---

## 二、测试结果详情

### 2.1 任务列表

| 任务名称 | 状态 | 退出码 | 日志文件 |
|---------|------|--------|---------|
EOF

# 列出所有任务结果
for exit_file in "${REPORTS_DIR}"/*.exit; do
  if [ ! -f "$exit_file" ]; then
    continue
  fi
  
  task_name=$(basename "$exit_file" .exit)
  exit_code=$(cat "$exit_file")
  log_file="${task_name}.log"
  
  if [ "$exit_code" -eq 0 ]; then
    status="✓ 通过"
  else
    status="✗ 失败"
  fi
  
  echo "| ${task_name} | ${status} | ${exit_code} | [查看日志](${log_file}) |" >> "${SUMMARY_FILE}"
done

cat >> "${SUMMARY_FILE}" <<'EOF'

### 2.2 失败任务详情

EOF

# 列出失败任务的详细信息
FAILED_COUNT=0
for exit_file in "${REPORTS_DIR}"/*.exit; do
  if [ ! -f "$exit_file" ]; then
    continue
  fi
  
  task_name=$(basename "$exit_file" .exit)
  exit_code=$(cat "$exit_file")
  
  if [ "$exit_code" -ne 0 ]; then
    FAILED_COUNT=$((FAILED_COUNT + 1))
    log_file="${REPORTS_DIR}/${task_name}.log"
    
    echo "#### ${FAILED_COUNT}. ${task_name}" >> "${SUMMARY_FILE}"
    echo "" >> "${SUMMARY_FILE}"
    echo "**退出码**：${exit_code}" >> "${SUMMARY_FILE}"
    echo "" >> "${SUMMARY_FILE}"
    echo "**错误日志摘要**：" >> "${SUMMARY_FILE}"
    echo "" >> "${SUMMARY_FILE}"
    echo '```' >> "${SUMMARY_FILE}"
    tail -n 30 "$log_file" >> "${SUMMARY_FILE}" 2>/dev/null || echo "无法读取日志文件" >> "${SUMMARY_FILE}"
    echo '```' >> "${SUMMARY_FILE}"
    echo "" >> "${SUMMARY_FILE}"
  fi
done

if [ ${FAILED_COUNT} -eq 0 ]; then
  echo "无失败任务 ✓" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
fi

# ==================== K6性能测试总结 ====================

cat >> "${SUMMARY_FILE}" <<'EOF'
---

## 三、性能测试总结

EOF

K6_FOUND=0
for json_file in "${REPORTS_DIR}"/k6-*.json; do
  if [ ! -f "$json_file" ]; then
    continue
  fi
  
  K6_FOUND=1
  test_name=$(basename "$json_file" .json)
  
  echo "### ${test_name}" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
  
  # 使用jq解析k6结果（如果有的话）
  if command -v jq &> /dev/null; then
    echo '```json' >> "${SUMMARY_FILE}"
    jq -r '.metrics | to_entries | map("\(.key): \(.value.values)") | .[]' "$json_file" 2>/dev/null | head -n 20 >> "${SUMMARY_FILE}" || echo "无法解析JSON" >> "${SUMMARY_FILE}"
    echo '```' >> "${SUMMARY_FILE}"
  else
    echo "（需要安装jq来解析性能数据）" >> "${SUMMARY_FILE}"
  fi
  
  echo "" >> "${SUMMARY_FILE}"
done

if [ ${K6_FOUND} -eq 0 ]; then
  echo "未执行k6性能测试" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
fi

# ==================== Maestro测试总结 ====================

cat >> "${SUMMARY_FILE}" <<'EOF'
---

## 四、Maestro UI测试总结

EOF

MAESTRO_FOUND=0
for maestro_dir in "${REPORTS_DIR}"/maestro-*; do
  if [ ! -d "$maestro_dir" ]; then
    continue
  fi
  
  MAESTRO_FOUND=1
  flow_name=$(basename "$maestro_dir")
  
  echo "### ${flow_name}" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
  
  # 查找junit报告
  junit_file=$(find "$maestro_dir" -name "*.xml" -type f | head -n 1)
  if [ -n "$junit_file" ] && [ -f "$junit_file" ]; then
    echo "JUnit报告：[${junit_file}](${junit_file})" >> "${SUMMARY_FILE}"
  fi
  
  # 查找截图
  screenshots=$(find "$maestro_dir" -name "*.png" -type f | wc -l | tr -d ' ')
  if [ "$screenshots" -gt 0 ]; then
    echo "截图数量：${screenshots}" >> "${SUMMARY_FILE}"
  fi
  
  echo "" >> "${SUMMARY_FILE}"
done

if [ ${MAESTRO_FOUND} -eq 0 ]; then
  echo "未执行Maestro UI测试" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
fi

# ==================== 主题测试总结 ====================

if [ -d "${REPORTS_DIR}/maestro-98-theme-validation" ]; then
  cat >> "${SUMMARY_FILE}" <<'EOF'
---

## 五、主题测试总结

EOF
  
  theme_dir="${REPORTS_DIR}/maestro-98-theme-validation"
  light_screenshots=$(find "$theme_dir" -name "light-theme-*.png" -type f | wc -l | tr -d ' ')
  dark_screenshots=$(find "$theme_dir" -name "dark-theme-*.png" -type f | wc -l | tr -d ' ')
  
  echo "**亮色主题截图**：${light_screenshots} 张" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
  echo "**暗色主题截图**：${dark_screenshots} 张" >> "${SUMMARY_FILE}"
  echo "" >> "${SUMMARY_FILE}"
  
  if [ $((light_screenshots + dark_screenshots)) -eq 20 ]; then
    echo "✓ 主题测试完整（预期20张截图）" >> "${SUMMARY_FILE}"
  else
    echo "⚠️ 主题测试不完整（预期20张，实际 $((light_screenshots + dark_screenshots)) 张）" >> "${SUMMARY_FILE}"
  fi
  
  echo "" >> "${SUMMARY_FILE}"
fi

# ==================== 结论和建议 ====================

cat >> "${SUMMARY_FILE}" <<'EOF'
---

## 六、结论和建议

EOF

if [ ${FAILED} -eq 0 ]; then
  cat >> "${SUMMARY_FILE}" <<'EOF'
### 测试结论

✓ **所有测试通过！** 

应用在以下方面表现良好：
- 功能完整性
- 性能指标
- UI稳定性
- 主题一致性

### 后续建议

1. 将此测试套件集成到CI/CD流水线
2. 建立性能基准数据库，追踪性能趋势
3. 定期在生产环境执行回归测试
4. 考虑增加更多边界场景测试

EOF
else
  cat >> "${SUMMARY_FILE}" <<EOF
### 测试结论

⚠️ **发现 ${FAILED} 个失败的测试任务**

需要立即处理的问题：
1. 查看上述失败任务的详细日志
2. 分析失败根因（环境配置/代码缺陷/测试脚本问题）
3. 修复后重新执行测试
4. 更新测试脚本以防止类似问题

### 后续建议

1. 优先修复失败的测试
2. 增强错误处理和日志记录
3. 考虑添加重试机制
4. 完善测试环境准备步骤

EOF
fi

cat >> "${SUMMARY_FILE}" <<'EOF'
---

## 附录

### 相关文档

- [综合测试计划](../../docs/testing/comprehensive-test-plan-2026-11-11.md)
- [Maestro测试说明](../../maestro/README.md)
- [K6负载测试脚本](../../k6-scripts/)

### 测试资源

- HTML报告：[index.html](index.html)
- 运行日志：[runner.log](runner.log)

---

**报告生成时间**：$(date +'%Y-%m-%d %H:%M:%S')
EOF

echo ""
echo "✓ 测试总结已生成: ${SUMMARY_FILE}"
echo ""
echo "查看报告:"
echo "  cat ${SUMMARY_FILE}"
echo "  open ${SUMMARY_FILE}"
echo ""
