# 待处理任务 - 节点健康探测修复跟踪

## 概述

本目录包含节点健康探测修复（P0）的所有待处理监控和分析任务。每个任务都采用 handoff 格式，包含完整的上下文、执行步骤和快速启动命令，可直接复制粘贴执行。

## 任务列表

### ✅ 已完成
- 代码实现与部署（2026-07-19 16:05）

### ⏳ 待执行

| 任务 | 执行时间 | 优先级 | 状态 | 文档 |
|------|---------|--------|------|------|
| 1小时监控检查 | 2026-07-19 17:05 | P0 | 待执行 | [01-1小时后监控检查.md](./01-1小时后监控检查.md) |
| 6小时效果评估 | 2026-07-19 22:00 | P0 | 待执行 | [02-6小时效果评估.md](./02-6小时效果评估.md) |
| 24小时完整分析 | 2026-07-20 16:00 | P0 | 待执行 | [03-24小时完整分析.md](./03-24小时完整分析.md) |
| 1周长期趋势分析 | 2026-07-26 16:00 | P0 | 待执行 | [04-1周长期趋势分析.md](./04-1周长期趋势分析.md) |

## 快速开始

### 自动执行提示

在对应时间点，新会话中粘贴以下内容即可自动执行：

#### 1小时后（17:05）
```
继续会话：节点健康探测修复 - 1小时监控检查

请执行 docs/待处理任务/01-1小时后监控检查.md 中的所有步骤。

关键上下文：
- 部署时间：2026-07-19 16:05
- 版本：phase0-complete-e56a3fe3-20260719-1171
- 环境变量：
  export SSH_KEY_154="$HOME/.ssh/184_id_rsa"
  export SSH_HOST_154="root@47.97.111.154"
  export SSH_PORT_154="25022"
```

#### 6小时后（22:00）
```
继续会话：节点健康探测修复 - 6小时效果评估

请执行 docs/待处理任务/02-6小时效果评估.md 中的所有步骤。
```

#### 24小时后（次日16:00）
```
继续会话：节点健康探测修复 - 24小时完整分析

请执行 docs/待处理任务/03-24小时完整分析.md 中的所有步骤。
```

#### 1周后（07-26 16:00）
```
继续会话：节点健康探测修复 - 1周最终报告

请执行 docs/待处理任务/04-1周长期趋势分析.md 中的所有步骤，并给出 P1/P2 优化建议。
```

## 任务依赖关系

```
部署完成
    ↓
01-1小时监控 (17:05)
    ↓
02-6小时评估 (22:00)
    ↓
03-24小时分析 (次日16:00)
    ↓
04-1周最终报告 (07-26 16:00)
    ↓
决策：P1优化 / 归档 / 调整参数
```

## 关键监控指标

每个任务都会检查以下指标：

### 新逻辑触发指标
- `cooldown_extended_due_to_failure` - 冷却期延长次数
- `recovered_with_actual_success` - 实际流量恢复次数
- `consecutive_3_failures_after_recovery` - 恢复后再失败次数

### 整体效果指标
- 请求总数
- 成功率（目标：提升到 85%+）
- 5xx 错误率

### 节点行为指标
- 商汤节点（Provider 15）失败/恢复事件
- NVIDIA NIM 节点（Provider 18）失败/恢复事件

## 环境变量（所有任务通用）

```bash
# SSH 连接
export SSH_KEY_154="$HOME/.ssh/184_id_rsa"
export SSH_HOST_154="root@47.97.111.154"
export SSH_PORT_154="25022"

# 项目路径
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
```

## 回滚预案

如果任何时间点发现问题，立即回滚：

```bash
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
export SSH_KEY_154="$HOME/.ssh/184_id_rsa"
bash scripts/deploy-seamless.sh rollback 154
```

**回滚触发条件**：
- ❌ 成功率下降 > 5%（低于 72%）
- ❌ 大面积服务不可用
- ❌ 节点全部长时间禁用（> 30 分钟）
- ❌ 出现未预期的错误日志

## 相关文档

### P0 修复文档
- [问题分析](../node-health-probe-mismatch-analysis.md)
- [修复总结](../node-health-probe-fix-summary.md)
- [部署报告](../deploy-production-complete-report.md)
- [初步跟踪](../tracking-analysis-initial-report.md)

### 生成的报告（将在执行后创建）
- `tracking-analysis-1hour-report.md`
- `tracking-analysis-6hour-report.md`
- `tracking-analysis-24hour-report.md`
- `tracking-analysis-1week-final-report.md`

## 实时监控命令

### 快速检查服务状态
```bash
ssh -i ~/.ssh/184_id_rsa -p 25022 root@47.97.111.154 \
  "curl -s http://localhost:8781/healthz" | jq .
```

### 实时查看节点事件
```bash
ssh -i ~/.ssh/184_id_rsa -p 25022 root@47.97.111.154 \
  'journalctl -u llm-gateway-go -f' | \
  grep --line-buffered -E '(disabled|recovered|cooldown)'
```

### 统计新逻辑触发次数
```bash
ssh -i ~/.ssh/184_id_rsa -p 25022 root@47.97.111.154 "
  echo -n '冷却期延长: '
  journalctl -u llm-gateway-go --since today --no-pager | \
    grep -c 'cooldown_extended_due_to_failure'
  
  echo -n '实际流量恢复: '
  journalctl -u llm-gateway-go --since today --no-pager | \
    grep -c 'recovered_with_actual_success'
"
```

## 注意事项

1. **执行顺序**：必须按顺序执行，每个任务依赖前一个任务的数据
2. **时间准确性**：尽量在指定时间点执行，保证数据的可比性
3. **保存输出**：每次执行都保存完整输出到对应的报告文档
4. **决策及时**：如果数据异常，立即执行回滚，不要等待下一个检查点
5. **上下文完整**：每个任务文档都是自包含的，可独立执行

## 任务完成标志

所有任务完成后：
1. 生成 1 周最终报告
2. 决定 P1/P2 优化方向
3. 归档所有文档到 `docs/archive/node-health-probe-p0-YYYYMMDD/`
4. 更新 CHANGELOG.md
5. 关闭本次修复任务

---

**创建时间**: 2026-07-19 16:25  
**创建者**: AI Agent (Kiro)  
**任务状态**: 进行中  
**预计完成**: 2026-07-26 16:00
