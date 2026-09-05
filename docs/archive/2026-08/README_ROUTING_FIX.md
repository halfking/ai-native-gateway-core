---
archived_from: (legacy) docs/archive/2026-08/README_ROUTING_FIX.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190917
status: archived
note: legacy archive, frontmatter retroactively added
---

# 路由节点状态修复 - 文档索引

> **问题**: "No available provider for model 'X'. All 0 candidates"  
> **状态**: ✅ 分析完成，修复方案就绪  
> **日期**: 2026-08-13

---

## 🚀 快速开始

### 1️⃣ 了解问题（5 分钟）
```bash
cat QUICK_REFERENCE.txt
```
一页纸快速参考，包含问题描述、根因、修复方案。

### 2️⃣ 应用修复（2-3 小时）
```bash
bash apply_p0_fixes.sh
```
交互式应用 P0 紧急修复（Ready Gate + LRU 优化 + 降级模式）。

### 3️⃣ 验证结果
```bash
# 编译测试
go test ./domains/ursm/v2/... -v
go test ./domains/streaming/executors/... -v

# 部署到 154
# (参考 FIX_ROUTING_NODE_STATUS.md 的部署流程)
```

---

## 📚 完整文档索引

### 核心文档（必读）

#### 1. 快速参考卡片 ⭐
**文件**: `QUICK_REFERENCE.txt`  
**用途**: 一页纸总结，快速了解问题和修复方案  
**阅读时间**: 5 分钟

**内容**:
- 问题描述
- 3 个根因
- 5 个修复方案（代码片段）
- 执行清单
- 验证步骤

---

#### 2. 任务完成报告 ⭐
**文件**: `TASK_COMPLETION_REPORT.md`  
**用途**: 完整的任务总结，包含做了什么、为什么、验证结果  
**阅读时间**: 15 分钟

**内容**:
- 做了什么（审计 + 修复方案设计）
- 改动清单（6 个文档 + 5 个代码修复）
- 为什么这样做（设计决策）
- 验证结果
- 遗留与风险
- 下一步建议

---

#### 3. 深度审计报告
**文件**: `ROUTING_NODE_STATUS_AUDIT_20260813.md`  
**用途**: 完整的代码审计，深入分析 7 个关键问题  
**阅读时间**: 30 分钟

**内容**:
- 问题现象和影响范围
- 候选节点获取流程分析
- 7 个关键问题详解
  - Ready Gate 过严
  - LRU 缓存命中率低
  - 候选节点双重过滤
  - 降级模式触发过严
  - 错误类型不明确
  - 冷却策略过激
  - 探活机制脱节
- 根因分析（3 个主次根因）
- 修复方案（P0 + P1）
- 预期改善数据

---

#### 4. 修复实施方案
**文件**: `FIX_ROUTING_NODE_STATUS.md`  
**用途**: 详细的修复步骤，包含代码 diff 和部署流程  
**阅读时间**: 20 分钟

**内容**:
- 修复清单（P0 3 个 + P1 2 个）
- 每个修复的详细代码 diff
- 验证步骤
- 部署流程（备份 → 部署 → 验证）
- 回滚计划
- 验收标准
- 风险评估

---

#### 5. 执行总结
**文件**: `SUMMARY_ROUTING_FIX.md`  
**用途**: 执行过程总结，交付物清单  
**阅读时间**: 10 分钟

**内容**:
- 已完成的工作
- 根因分析
- 修复方案概要
- 预期改善数据
- 下一步行动
- 交付物清单
- 团队协作事项

---

### 工具脚本

#### 6. 诊断脚本
**文件**: `diagnose_routing.sh`  
**用途**: 自动扫描代码库，生成诊断报告  
**执行时间**: 1 分钟

**功能**:
- 检查 URSM v2 配置（LRU 容量、TTL）
- 定位 Ready Gate 逻辑
- 定位降级模式触发条件
- 定位错误消息生成位置
- 统计关键代码引用
- 生成诊断总结

**运行**:
```bash
bash diagnose_routing.sh
# 输出: routing_diagnosis_YYYYMMDD_HHMMSS.log
```

---

#### 7. P0 修复应用脚本 ⭐
**文件**: `apply_p0_fixes.sh`  
**用途**: 交互式应用 P0 修复，自动备份  
**执行时间**: 10 分钟（交互式）

**功能**:
- 自动备份原文件
- 应用 Ready Gate Fail-Open
- 应用 LRU 缓存优化
- 应用降级模式改进
- 编译验证
- 提供回滚方法

**运行**:
```bash
bash apply_p0_fixes.sh
# 交互式提示，按 Enter 确认每步
```

---

## 📊 修复优先级

### P0 - 紧急修复（今天，2-3 小时）

| 修复 | 文件 | 改动 | 效果 |
|------|------|------|------|
| Ready Gate Fail-Open | `domains/ursm/v2/manager.go` | ~10 行 | Redis 故障时 85% 请求仍可成功 |
| LRU 缓存优化 | `domains/ursm/v2/config.go` | ~2 行 | 缓存命中率 20% → 85% |
| 降级模式改进 | `domains/streaming/executors/router.go` | ~1 行 | 零候选时降级兜底 |

### P1 - 重要修复（本周，2-3 小时）

| 修复 | 文件 | 改动 | 效果 |
|------|------|------|------|
| 分级冷却策略 | `domains/ursm/v2/reducer/reducer.go` | ~50 行新增 | 偶发故障 5min → 30s |
| 错误类型细化 | `domains/streaming/handler.go` | ~30 行新增 | 明确失败原因 |

---

## 🎯 预期改善

| 指标 | 修复前 | 修复后 | 改善 |
|------|--------|--------|------|
| **可用性** | 99.90% | 99.95% | +0.05% |
| **P99 延迟** | 800ms | 750ms | -50ms |
| **缓存命中率** | 20% | 85% | +65% |
| **错误率** | 5% | 1% | -80% |
| **偶发故障成功率** | 85% | 95% | +10% |

**业务价值**:
- 每天减少约 **10,000 次** "No available provider" 错误
- Redis 故障时 **85% 请求**仍可通过缓存成功
- 偶发超时后 **30 秒内**恢复（vs 当前 5 分钟）

---

## 🔄 执行流程

### Step 1: 阅读文档（15 分钟）
```bash
# 快速了解
cat QUICK_REFERENCE.txt

# 深入理解（可选）
cat ROUTING_NODE_STATUS_AUDIT_20260813.md
```

### Step 2: 应用修复（2-3 小时）
```bash
# 交互式应用 P0 修复
bash apply_p0_fixes.sh

# 或手动修改（参考 FIX_ROUTING_NODE_STATUS.md）
```

### Step 3: 编译测试（30 分钟）
```bash
# 编译
go build -o gateway cmd/gateway/main.go

# 单元测试
go test ./domains/ursm/v2/... -v
go test ./domains/streaming/executors/... -v
```

### Step 4: 部署验证（1 小时）
```bash
# 备份
ssh root@8.136.114.154 -p 25022 "cp /opt/llm-gateway-go/gateway /opt/llm-gateway-go/gateway.backup.$(date +%Y%m%d_%H%M%S)"

# 部署
scp -P 25022 gateway root@8.136.114.154:/opt/llm-gateway-go/gateway
ssh root@8.136.114.154 -p 25022 "systemctl restart llm-gateway-go"

# 验证
ssh root@8.136.114.154 -p 25022 "journalctl -u llm-gateway-go -f"
```

---

## 🔍 故障排查

### 问题 1: 编译失败
**现象**: `go build` 报错  
**解决**: 检查代码修改是否正确，参考 `FIX_ROUTING_NODE_STATUS.md` 的代码 diff

### 问题 2: 单元测试失败
**现象**: `go test` 失败  
**解决**: 可能需要更新测试用例，参考审计报告中的逻辑变化

### 问题 3: 部署后无效果
**现象**: 错误率未下降  
**解决**: 检查 Prometheus 指标，确认修复是否生效

---

## 📞 支持

### 文档问题
- 查看 `TASK_COMPLETION_REPORT.md` 的"遗留与风险"章节
- 检查 `routing_diagnosis_*.log` 诊断日志

### 代码问题
- 查看 `.backup_*/` 目录恢复原文件
- 参考 `FIX_ROUTING_NODE_STATUS.md` 的回滚计划

### 部署问题
- 1 分钟回滚：
  ```bash
  ssh root@8.136.114.154 -p 25022 "systemctl stop llm-gateway-go && \
    cp /opt/llm-gateway-go/gateway.backup.* /opt/llm-gateway-go/gateway && \
    systemctl start llm-gateway-go"
  ```

---

## 📅 时间线

| 时间 | 任务 | 状态 |
|------|------|------|
| 2026-08-13 | 深度代码审计 | ✅ 完成 |
| 2026-08-13 | 修复方案设计 | ✅ 完成 |
| 2026-08-13 | 文档和脚本开发 | ✅ 完成 |
| 2026-08-13 | **应用 P0 修复** | ⏳ 待执行 |
| 2026-08-13 | 部署到 154 | ⏳ 待执行 |
| 2026-08-14-20 | 7 天观察期 | ⏳ 待执行 |
| 2026-08-21 | 应用 P1 修复 | ⏳ 待执行 |

---

## ✅ 检查清单

### 执行前
- [ ] 阅读 `QUICK_REFERENCE.txt` 了解问题
- [ ] 阅读 `FIX_ROUTING_NODE_STATUS.md` 了解修复步骤
- [ ] 确认有 154 服务器访问权限

### 执行中
- [ ] 运行 `bash apply_p0_fixes.sh` 应用修复
- [ ] 编译成功 `go build`
- [ ] 单元测试通过 `go test`
- [ ] 备份原文件
- [ ] 部署到 154
- [ ] 服务启动成功

### 执行后
- [ ] Prometheus 指标正常
- [ ] 错误率下降
- [ ] 延迟下降
- [ ] 无新的异常日志
- [ ] 7 天观察期无问题

---

## 🎓 关键学习

### 设计模式
- **Fail-Open Pattern** - 依赖故障时使用缓存兜底
- **分级冷却** - 根据失败模式动态调整冷却时间
- **降级模式** - 零候选时的最后保护机制

### 性能优化
- **LRU 缓存** - 容量和 TTL 的数据驱动决策
- **减少回源** - 缓存命中率从 20% → 85%
- **减少双重过滤** - 避免重复检查

### 可观测性
- **新增指标** - `node_mirror_fallback` 监控缓存兜底
- **详细日志** - 冷却原因、降级触发
- **明确错误** - 区分 cooling / rate_limit / quota

---

**最后更新**: 2026-08-13  
**维护者**: LLM Gateway Team  
**文档版本**: v1.0
