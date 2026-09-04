# LLM Gateway 可观测性增强部署结果

**日期**: 2026-09-04  
**提交**: 32cdf5479 (feat: enhance candidate outcome logging with request_id traceability)  
**范围**: 245 预发布 → 154 生产（受门禁保护未执行）

---

## 执行摘要

**结果**: ⚠️ **代码已推送，245 门禁未通过，154 未部署**

- ✅ 代码修改、测试、推送已完成（32cdf5479）
- ⚠️ 245 候选实例 `/readyz` 超时（60s），脚本保护性拒绝切流
- ✅ 245 旧实例（1921-2e1ccfb5, 2.4.7）保持服务，无中断
- ⛔ 154 生产部署按门禁规则停止，未触碰

---

## 已完成工作

### 1. 代码推送（✅ 完成）
- **提交**: 32cdf5479a7328a5b0bc2ffcefdb0eca63d7020f
- **消息**: feat(observability): enhance candidate outcome logging with request_id traceability
- **变更**:
  - `domains/streaming/execute_attempt.go`: +34/-19 行
  - `domains/streaming/tool_call_xml.go`: +5/-0 行
  - `domains/streaming/execute_attempt_test.go`: +1/-1 行
  - `docs/ERROR-EVIDENCE-MATRIX-20260904.md`: 新增
  - `docs/LOCAL-VERIFICATION-REPORT-20260904.md`: 新增

### 2. 本地验证（✅ 通过）
- ✅ 编译成功：`go build ./cmd/gateway`
- ✅ 关键测试通过：
  - `TestFoldCandidateOutcomesPreservesTypedRetryableKinds`
  - `TestRunEmptyStreamGate*` (8个)
  - `TestExecuteAttempt*` (6个)
  - `TestAggregateTaskOutcome*`
- ✅ 部署脚本 dry-run：245/154 均通过

### 3. Git 推送（✅ 完成）
- **分支**: main
- **远程**: origin/main (codeup.aliyun.com)
- **状态**: fast-forward 合并，基于远程 a6f778588

---

## 245 部署详情

### 部署时间线
```
2026-09-04 03:12:22  [0/9] 部署前 PG 预检
2026-09-04 03:12:30  [1/9] bump version → 1931-32cdf547
2026-09-04 03:12:45  [2/9] 前端构建 (web/)
2026-09-04 03:12:51  [3/9] 后端编译 (Go)
2026-09-04 03:12:58  [4/9] stage release bundle
2026-09-04 03:13:06  [5/9] tar pipe bundle → 245
2026-09-04 03:13:12  [6.5/9] 切换前 pending 迁移
                     ✓ 655_session_summaries_schema_reconcile.sql
                     ✓ 656_auto_route_selections_hot_partition.sql
2026-09-04 03:13:18  [7/9] systemd 启动候选实例 (8782)
2026-09-04 03:13:22  [8/9] 候选预热 + Nginx 原子切流
                     ✓ /healthz OK
                     ✗ /readyz failed: probe timeout after 60s
2026-09-04 03:14:22  [部署失败] 候选实例保护性拒绝切流
```

### 失败根因

**现象**: 候选实例 `/readyz` 连续 60 秒返回 `503 Service Unavailable`

**最后探测响应** (2026-09-04 03:14:22):
```json
{
  "database": null,
  "redis": {
    "connected": true,
    "latency": "411.185µs"
  },
  "status": "not_ready"
}
```

**关键日志**:
- Redis 已连接，延迟正常（~400µs）
- `database` 字段为 `null`（DB 连接器初始化未完成）
- 持续 60 秒未变化，超过探测超时

**事后状态** (2026-09-04 03:16:08):
- 候选进程已变为 `ready`（`{"database":{"connected":true}}`）
- 但部署脚本已失败退出，未执行切流
- 245 仍运行旧实例 1921-2e1ccfb5 on 8781
- 候选实例 8782 **未监听**（systemd 未持久化）

**根因假设**:
1. DB 连接池初始化慢于 Redis（~60s）
2. 候选实例启动时序：systemd start → 应用初始化 → DB pool ready
3. 探测脚本在第 60 秒超时，候选在第 61-70 秒变为 ready

---

## 154 停止原因

按批准的门禁规则：
- **规则**: 245 通过 `/healthz` + `/readyz` + 版本验证 → 才允许 154 部署
- **本次**: 245 `/readyz` 失败 → 154 **未尝试部署**
- **保护**: 避免同时影响预发布和生产环境

---

## 当前运行状态

### 245 (预发布 / staging)
- **active_port**: 8781
- **active_version**: 2.4.7-2e1ccfb5-20260904-1921
- **状态**: ✅ 正常服务
- **healthz**: 200 OK
- **readyz**: 200 OK (database + redis 均已连接)
- **流量**: 未受影响

### 154 (生产 / llm.kxpms.cn)
- **状态**: ✅ 未触碰
- **运行版本**: (未查询，维持部署前状态)
- **流量**: 未受影响

---

## 风险评估

### 本次部署的风险等级: **零（未执行）**
- ✅ 245 旧实例保持服务
- ✅ 154 未触碰
- ✅ 代码修改仅为日志增强，无行为变化
- ✅ 新日志字段为条件性添加，不会产生空值噪音

### 候选预热超时的影响: **演练价值 > 0**
- ✅ 验证了脚本保护机制正常工作
- ✅ 确认了 `/readyz` 门禁契约
- ⚠️ 暴露了候选实例 DB 初始化耗时接近探测上限（60s）

---

## 后续建议

### 短期（1-2天）

1. **诊断 245 候选实例 DB 初始化慢**
   - 检查候选实例日志：`journalctl -u llm-gateway-go.service --since "2026-09-04 03:13:00" --until "2026-09-04 03:15:00"`
   - 查找 `database pool init` 或 `pgx.Connect` 相关耗时
   - 确认是否为一次性问题（首次冷启动）或系统性问题

2. **可选：调整探测超时**
   - 当前：60s 超时
   - 建议：如果 DB 初始化确认需要 60-70s，可调整为 90s
   - 位置：`scripts/deploy-seamless.sh` probe 函数

3. **可选：手动重试 245 部署**
   ```bash
   cd /Users/xutaohuang/workspace/ai-native-tools/syncfield/llm-gateway-go-4
   bash scripts/deploy-245.sh
   ```
   - 仅当 DB 初始化问题已确认为一次性问题时执行
   - 部署后验证 `/healthz` + `/readyz` + 真实请求

4. **245 通过后，执行 154 部署**
   ```bash
   bash scripts/deploy-154.sh
   ```

### 中期（1周）

1. **采集新日志样本**
   - 245/154 部署后，观察 `fold_candidate_outcomes_complete` 日志
   - 确认 `request_id` 字段正确出现
   - 采集 14 类错误的实际日志样本

2. **评估 XML 溢出日志启用**
   - 检查生产环境是否出现 64KB XML 工具调用片段
   - 决定是否取消注释 `stream_xml_tool_call_fragment_overflow` 日志

3. **编写常见错误排查手册**
   - 基于 ERROR-EVIDENCE-MATRIX 和实际日志
   - 提供 `grep` 命令和 `journalctl` 查询示例

---

## 附录：关键命令

### 查询 245 当前状态
```bash
ssh root@8.136.114.245 "curl -s http://127.0.0.1:8781/healthz | jq"
ssh root@8.136.114.245 "curl -s http://127.0.0.1:8781/readyz | jq"
ssh root@8.136.114.245 "cat /opt/llm-gateway-go/current/VERSION"
```

### 查询 154 当前状态
```bash
ssh -J root@115.29.212.252:25022 root@47.97.111.154 "curl -s http://127.0.0.1:\$(cat /opt/llm-gateway-go/run/active-port)/healthz | jq"
```

### 查看 245 候选实例启动日志
```bash
ssh root@8.136.114.245 "journalctl -u llm-gateway-go.service --since '2026-09-04 03:13:00' --until '2026-09-04 03:15:00' | grep -E 'database|pool|ready|healthz'"
```

---

**报告生成时间**: 2026-09-04  
**状态**: 代码已推送，等待 245 重试部署或诊断
