---
archived_from: (legacy) docs/archive/2026-07/REALTIME_STREAM_FIX_SUMMARY.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190919
status: archived
note: legacy archive, frontmatter retroactively added
---

# 实时请求流数据问题修复总结

## 已完成工作 ✅

### Phase 1: probe-direct 请求缺失数据修复

**问题**: probe-direct 类型的探测请求（如 `probe-direct-c25-mglm-5.2-a1-ok-1784436521598380984`）在请求详情中没有：
- request_body（请求消息）
- response_body（响应内容）
- prompt_tokens / completion_tokens（token 统计）

**根因**: `bg/active_probe_emitter.go` 在构建 `telemetry.RequestLogEntry` 时，虽然 `ProbeResult` 已包含这些数据，但从未传递给数据库写入逻辑。

**修复内容**:
```go
// bg/active_probe_emitter.go (line 123-137)
var requestBody, responseBody *string
if result.RequestBody != "" {
    requestBody = strPtrTelemetry(result.RequestBody)
}
if result.ResponseBody != "" {
    responseBody = strPtrTelemetry(result.ResponseBody)
}

entry := &telemetry.RequestLogEntry{
    // ... 现有字段
    RequestBody:  requestBody,   // ← 新增
    ResponseBody: responseBody,  // ← 新增
}
```

**测试验证**: ✅ 通过
```bash
$ go test -v -run "TestProbeResult|TestProbeEmitter" ./bg/
PASS: TestProbeResultContainsRequestResponseBodies
PASS: TestProbeEmitterBuildsEntryWithBodies
```

**影响评估**:
- ✅ 零破坏性 - 只填充之前为 NULL 的字段
- ✅ 向后兼容 - 旧数据保持不变
- ✅ 性能影响可忽略 - 每个 probe 增加 ~100-500 字节

---

## 待实施工作 🚧

### Phase 2: 请求刷新延迟修复（优先级 P0）

**问题**: 前端请求到达网关后，延迟 5 秒才能在实时流中看到

**根因**:
1. INSERT 时机太晚 - 只在响应完成后才写入
2. 批处理延迟 - 200ms 窗口 + 最多 50 条才 flush
3. 队列背压 - 高峰时堆积

**修复方案**: 双写策略
- 请求开始时：立即 INSERT（in_progress 状态）
- 请求完成时：UPDATE 完整数据

**预期效果**: 延迟从 5s 降低到 <1s

---

### Phase 3: 前端泳道展示稳定性（优先级 P1）

**问题**: 泳道图数据大幅跳变，不像增量更新

**根因**:
1. 查询跨分区边界导致时间窗口不连续
2. SSE 推送与轮询数据不一致
3. 前端全量替换而非增量合并

**修复方案**:
1. 后端：统一查询 request_logs_hot（避免跨分区）
2. 前端：改为增量更新策略

---

## 部署步骤

### 立即部署（Phase 1）

```bash
# 1. 编译
cd /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go
go build -o llm-gateway-go ./cmd/gateway

# 2. 部署到 71 服务器
scp llm-gateway-go root@192.168.31.71:/opt/llm-gateway-go/
ssh root@192.168.31.71 "systemctl restart llm-gateway-go"

# 3. 验证新 probe 请求包含完整数据
psql -h 192.168.31.71 -U llm_gateway -d llm_gateway -c "
SELECT 
    request_id,
    length(request_body::text) as req_len,
    length(response_body::text) as resp_len,
    prompt_tokens,
    completion_tokens
FROM request_logs_hot
WHERE request_id LIKE 'probe-direct-%'
  AND ts > NOW() - INTERVAL '10 minutes'
ORDER BY ts DESC
LIMIT 5;"

-- 期望：req_len > 0, resp_len > 0, tokens > 0
```

### 后续部署（Phase 2 & 3）

等待 Phase 1 验证通过并稳定运行后，再实施 Phase 2 和 Phase 3。

---

## 回滚方案

如果出现问题（极低概率）：

```bash
ssh root@192.168.31.71
cd /opt/llm-gateway-go
cp llm-gateway-go.backup llm-gateway-go
systemctl restart llm-gateway-go
```

影响：新的 probe 行会恢复到 request_body/response_body = NULL 的状态。

---

## 相关文件

### 修改的文件
- `bg/active_probe_emitter.go` - 核心修复
- `bg/active_probe_emitter_fix_test.go` - 测试验证

### 新增的文档
- `docs/design/realtime-request-stream-fix.md` - 详细设计文档
- `docs/fixes/2026-07-19-realtime-request-stream-fixes.md` - 修复总结
- `REALTIME_STREAM_FIX_SUMMARY.md` - 本文档

---

## 关键指标

**修复前**:
- probe-direct 请求：request_body = NULL, response_body = NULL
- 请求刷新延迟：5-10 秒
- 前端泳道跳变：频繁

**修复后（Phase 1）**:
- ✅ probe-direct 请求：包含完整 request_body 和 response_body
- ⏳ 请求刷新延迟：待 Phase 2 修复
- ⏳ 前端泳道跳变：待 Phase 3 修复

---

## 下一步行动

1. ✅ **立即部署 Phase 1** 到 71 服务器
2. 🔍 **监控 24 小时** - 确认无副作用
3. 📋 **规划 Phase 2** - 双写策略详细实现
4. 📋 **规划 Phase 3** - 前后端协同修复

---

**修复完成时间**: 2026-07-19  
**修复工程师**: AI Agent  
**审核状态**: 待人工审核
