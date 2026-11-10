# 🔄 会话交接：被动指纹断链修复审计

**任务标识**: P0-B 被动时序指纹采集断链修复审计与验证  
**交接时间**: 2026-11-10T08:30:00+08:00  
**优先级**: P0（核心子系统完全失效）  
**工作目录**: `/Users/niuer/workspace/ai-native-tools/llm-gateway/llm-gateway-go`

---

## 📋 任务概要

**使命**: 审计并修正 P0-B 被动时序指纹采集断链修复的实现；验证修复正确性并完成部署验证。

**背景**: 被动时序指纹子系统（migration 955、第 5 维度 DimPassive）在生产环境中完全失效，`passive_model_fingerprints` 表恒为空。根因是采样代码被错误嵌套在 `if len(result.ResponseBody) > 0` 分支内，而流式请求的 `ExecuteResult` 不包含 `ResponseBody` 字段，导致流式请求（唯一有 ITT/TTFT 数据的流量）永不采样。

**实证**: 211 生产主机 184 个流式请求/10分钟，worker 正常运行，但基线表 0 行。

---

## ✅ 已完成工作

### 1. 根因定位与实证
- **断链点**: `domains/streaming/handler.go:7021-7042`（原位置）的采样块嵌套在 `if len(result.ResponseBody) > 0` 内
- **机制**: 流式请求的 `ExecuteResult` 不填充 `ResponseBody`（见 `executors/executor_chat.go:2489-2506`）
- **影响**: 100% 流式流量零采样 → 基线表恒空 → DimPassive 无数据
- **实证**: 211 生产查询显示 184 流式请求/10min、worker 运行、0 基线行

### 2. 代码修复实现
**文件**: `domains/streaming/handler.go`

**修改 1**: 新增 `ChatHandler.samplePassiveTiming()` 方法（1899-1960 行）
- 70 行文档注释：明确位置纪律（必须顶层调用）、键口径、token 来源
- 60 行实现：nil 安全、使用 `BindingRawModel()` 作键、从 capture 读 completion_tokens

**修改 2**: 移动调用点到 `emitTelemetry` 顶层（7135-7145 行）
- 位置：quality signals 之后、ResponseBody 提取之前
- 适用：流式 + 非流式（非流式 capture=nil → 提前返回）

**修改 3**: 移除旧代码块
- 原嵌套位置（7021-7042）的采样逻辑已移除
- 保留说明注释（7160-7164）

### 3. 键口径与设计决策
- **基线键**: `result.Candidate.BindingRawModel()` = `model_offers.raw_model_name`（如 "MiniMax-M3"）
- **理由**: 与仓库既有「按绑定键状态」纪律一致（见 `provider/client.go:95-106`）
- **fallback**: `evt.CanonicalName`（覆盖 BindingRawModel 空的边缘情况）
- **token 来源**: `capture.SummaryAsMap()["completion_tokens"]`（流式路径的正确来源）

### 4. 文档与验证
- **修复文档**: `docs/审计/P0-B_被动指纹断链修复_2026-11-10.md`（完整的根因、修复、验证计划）
- **编译验证**: ✅ `go build` 和 `go test -c ./domains/streaming` 通过

---

## 🎯 待办事项（优先级排序）

### 高优先级（当前会话）

1. **代码审计** ⭐⭐⭐
   - [ ] 复核 `samplePassiveTiming()` 实现逻辑
     - nil 检查是否完整（h、passiveSink、capture、result）
     - 键逻辑是否正确（BindingRawModel → CanonicalName fallback）
     - completion_tokens 读取是否健壮（类型断言、零值处理）
   - [ ] 复核调用位置是否正确
     - 确认在 `emitTelemetry` 顶层（不在任何 if 分支内）
     - 确认在 capture 完成后（流已结束、usage 已累积）
   - [ ] 检查旧采样代码是否完全移除
     - 搜索 `if len(result.ResponseBody) > 0` 内是否还有 passiveSink.Submit 调用

2. **Git 提交** ⭐⭐⭐
   - [ ] 按仓库规范提交（中文 commit message，包含审计编号 P0-B）
   - [ ] commit message 示例：
     ```
     [P0-B] 修复被动时序指纹采集断链

     根因：采样块嵌套在 if len(result.ResponseBody) > 0 内，
     流式请求 ResponseBody 空导致永不采样 → 955 表恒空。

     修复：
     - 提取 samplePassiveTiming() 方法到 handler 顶层
     - 使用 BindingRawModel() 作基线键
     - 从 capture.SummaryAsMap() 读 completion_tokens

     影响：恢复被动指纹子系统（migration 955、DimPassive）
     验证：待 211 部署后观测基线表有行、worker 输出 flushed
     ```

3. **245/154 部署验证** ⭐⭐
   - [ ] 部署到 245 预发布环境
   - [ ] 部署到 154 预发布环境
   - [ ] 验证基线表开始有行（5-10 分钟窗口）

### 中优先级（后续会话）

4. **211 生产部署与验证** ⭐⭐
   - [ ] 部署到 211 生产环境
   - [ ] 执行验证计划（见下方「验证计划」章节）
   - [ ] 观测 5-10 分钟窗口的指标

5. **后续改进** ⭐
   - [ ] 增加 collector 的 `submittedCount` 指标（heartbeat 报告）
   - [ ] 增加 handler 单元测试：流式请求调用 samplePassiveTiming
   - [ ] 增加集成测试：流式请求 → 基线表有行
   - [ ] DimPassive 对接扫描器（需名字空间桥接：`provider_models.raw_model_name` ↔ `model_offers.raw_model_name`）

---

## 🔍 验证计划（211 部署后）

### SQL 查询（5-10 分钟窗口）

```sql
-- 1. 基线表行数（5 分钟后应 > 0）
SELECT count(*) FROM passive_model_fingerprints;

-- 2. 基线内容检查
SELECT 
    credential_id, model_id, sample_count, 
    ttft_p50_ms, itt_p50_ms, throughput_mean,
    window_from, window_to
FROM passive_model_fingerprints
ORDER BY window_to DESC LIMIT 5;

-- 3. 样本质量检查
SELECT 
    model_id,
    sample_count,
    ttft_p50_ms,
    itt_p50_ms,
    throughput_mean,
    (window_to - window_from) AS window_duration
FROM passive_model_fingerprints
WHERE window_to > now() - interval '30 minutes'
ORDER BY sample_count DESC LIMIT 10;

-- 4. 流式请求流量（对照组）
SELECT count(*) FROM session_turns_hot 
WHERE ts > now() - interval '10 minutes' 
  AND COALESCE(stream_chunk_count,0) > 0;
```

### Worker 日志检查

```bash
# 211 gateway 容器日志
docker logs --since 10m llm-gateway-211-a 2>&1 | grep -i "passive baseline"

# 预期输出（每 5 分钟）:
# {"msg":"passive baseline flushed","flushed":3,"elapsed_ms":...}
# {"msg":"passive baseline worker alive","tick":6,"submitted":...}
```

### 成功标准
- ✅ 表行数 > 0（5 min 后）
- ✅ worker 每 5 min 输出 "flushed" 日志，`flushed > 0`
- ✅ sample_count 分布合理（主力模型 >= 10 samples/5min）
- ✅ 时序指标数值范围正常：
  - TTFT: 500-3000ms
  - ITT: 50-300ms
  - throughput: 5-50 tok/s

---

## ⚠️ 风险与注意事项

### 代码风险
1. **未执行单元测试**: 只验证了编译通过，逻辑可能有缺陷
2. **旧代码块可能未完全移除**: 需复查 handler.go:7160 附近是否还有残留的 passiveSink.Submit 调用
3. **nil 安全**: result 可能为 nil（失败请求），需确认方法入口处有 nil 检查

### 名字空间风险
- **基线键（model_offers）与扫描器键（provider_models）不同**
- **影响**: DimPassive 对接扫描器时需要额外桥接逻辑
- **桥接方案**: 通过 `credential_model_bindings` 连接两个名字空间（已记录在 handler.go:1924-1928 注释）

### 部署风险
- **未部署验证**: 修复效果需生产环境观测才能确认
- **降级方案**: 若 5 min 后仍 0 行 → 检查 worker 日志报错；若有行但 sample_count 全是 1-2 → ExtractPassiveFromTracker 过滤过严

---

## 📚 关键参考文件

### 主修改文件
- `domains/streaming/handler.go`
  - 新增方法：1899-1960 行（`samplePassiveTiming`）
  - 调用点：7135-7145 行（`emitTelemetry` 顶层）
  - 旧位置注释：7160-7164 行

### 相关文件
- `domains/modelcredibility/passive_collector.go`（采集器，未修改）
- `bg/passive_baseline_worker.go`（worker，未修改）
- `provider/client.go:95-106`（`BindingRawModel` 方法定义）
- `domains/streaming/executors/executor_chat.go:2489-2506`（流式成功路径不含 ResponseBody 的根因证据）

### 文档
- `docs/审计/P0-B_被动指纹断链修复_2026-11-10.md`（完整修复文档）
- `/tmp/handoff-summary-passive-fix.json`（本次交接摘要）

---

## 🎓 技术决策记录

### 决策 1：键口径
- **选择**: `result.Candidate.BindingRawModel()` (model_offers.raw_model_name)
- **理由**: 与仓库既有「按绑定键状态」纪律一致（见 `provider.Candidate.BindingRawModel()` 注释："State keyed by RawModel would merge their independent health telemetry"）
- **权衡**: 与扫描器的 provider_models.raw_model_name 名字空间不同，后续需桥接

### 决策 2：token 来源
- **选择**: `capture.SummaryAsMap()["completion_tokens"]`
- **理由**: 流式路径的 SSE usage 块累积到 capture，是流式请求的正确来源
- **权衡**: 非流式请求也使用此源（reqLog.CompletionTokens 可能更直接），但非流式的 capture=nil 会提前返回，无影响

### 决策 3：调用位置
- **选择**: `emitTelemetry` 顶层，quality signals 之后、ResponseBody 提取之前
- **理由**: 此时 capture 已完成（流已结束），usage 已累积；且在顶层确保流式和非流式都执行
- **权衡**: 在 token 提取之前调用，但 token 来自 capture 而非 reqLog，无依赖

---

## 🔄 会话状态

**Git 状态**:
```
M  domains/streaming/handler.go
M  web/public/menu-config.json
?? docs/审计/
?? HANDOFF-error-audit-2026-11-10.md
```

**未提交修改**: handler.go 的修复 + 审计文档（未纳入 git）

**下一步**: 代码审计 → Git 提交 → 部署验证

---

## 💡 提示与建议

1. **代码审计重点**:
   - 检查 `samplePassiveTiming` 的 nil 安全（特别是 result.Candidate 的访问）
   - 搜索 `passiveSink.Submit` 确认旧代码已完全移除
   - 验证调用位置不在任何 if 分支内

2. **提交规范**:
   - 使用中文 commit message
   - 包含审计编号 [P0-B]
   - 说明根因、修复、影响

3. **部署顺序**:
   - 先部署 245/154 预发布环境验证
   - 确认基线表有行后再部署 211 生产

4. **验证窗口**:
   - 首次 flush 在部署后 5 分钟（worker flush_interval=5m）
   - 观测 10 分钟窗口以覆盖 2 个 flush 周期

---

**交接完成时间**: 2026-11-10T08:30:00+08:00  
**预计下一检查点**: 代码审计完成并提交（~30 分钟）  
**最终目标**: 211 生产验证通过（基线表有行、指标正常）
