# 24小时代码修正并行审计计划

## 审计时间窗口
- 开始：2026-09-03 16:00
- 结束：2026-09-04 16:20
- 基线提交：`8fb5014c0` → `cb160f2f6`

## 审计目标
1. 流程闭环：请求处理从入口到出口的完整性和可追踪性
2. 数据闭环：数据在传输、存储、转换过程中的完整性和一致性
3. 反馈闭环：错误捕捉、日志、指标和告警的完整性
4. 安全性：并发控制、资源管理、错误处理
5. 可维护性：冗余代码识别、代码结构优化

## 并行审计任务分配

### Agent 1: Dispatch调度系统审计
**责任范围**：
- `bg/dispatch.go`、`bg/backend.go`、`bg/model_tier.go`
- composition-root初始化顺序
- Backend生命周期管理（Stop并发安全）
- DimensionIndex正确性
- QueuedRequest并发安全

**关键提交**：
- `e5476c2cb`: fix(dispatch): close backend and lifecycle edge cases
- `f8af3a0cf`: fix(dispatch): composition-root ordering, DimensionIndex correctness
- `7189ccac7`: merge: dispatch composition lifecycle fixes

**审计检查项**：
1. 初始化顺序是否正确（DimensionIndex → Backend → ModelTier）
2. Backend.Stop是否使用sync.Once防止并发panic
3. context继承是否正确
4. 资源泄漏检查（goroutine、channel、连接）
5. 并发测试覆盖率
6. 日志和指标完整性

### Agent 2: Analytics与MV审计
**责任范围**：
- `admin/analytics.go`、`admin/analytics_materialized.go`
- `admin/funnel_cache.go`
- routing stats数据源对齐
- tenant-scope funnel cache
- probe流量排除

**关键提交**：
- `a046f7739`: fix(analytics): tenant-scope funnel cache and exclude probe traffic
- `2e1ccfb5f`: fix(analytics): align routing stats source and harden MV rebuild
- `be4bd0f48`: fix(mv-consistency): use routing_analytics_source for MV drift checks
- `efd6b0134`: fix(analytics): exclude self-check traffic from routing heatmap

**审计检查项**：
1. 数据源一致性（request_logs_hot vs routing_analytics_source）
2. MV重建的原子性和幂等性
3. tenant隔离正确性
4. probe流量过滤逻辑完整性
5. funnel cache的并发安全
6. 错误处理和降级策略

### Agent 3: 凭据加密与部署门控审计
**责任范围**：
- `secret/aes_gcm.go`
- `admin/handler.go`、`admin/handler_cred_encrypt_test.go`
- `scripts/deploy-seamless.sh`、`scripts/deploy-lib/post-deploy-verify.sh`
- `tests/deploy_credential_decrypt_verify_test.sh`

**关键提交**：
- `8a27d7daa`: fix(secret): unwrap v1:legacy fernet envelopes and keep ErrDecrypt
- `811824453`: feat(deploy): gate blue-green handoff on credential decrypt smoke
- `524240125`: fix(deploy-local): auto-load .env.local and fail closed on unpublished PG port

**审计检查项**：
1. v1:legacy fernet解密兼容性
2. ErrDecrypt错误传播链路
3. 部署烟雾测试门控逻辑
4. 凭据解密验证的fail-closed机制
5. 环境变量加载顺序和安全性
6. 错误日志结构化

### Agent 4: 凭据管理与Console审计
**责任范围**：
- `admin/handler.go`（凭据相关）
- credential_model_index_hot并发写入
- Provider Console权限控制
- 凭据质量聚合

**关键提交**：
- `127d8c2f5`: fix(credential-console): align secret permissions and readonly controls
- `4a43f922f`: merge: credential console audit fixes
- `8b3f3cc9c`/`8472c99e6`: fix(db): credential_model_index_hot concurrent-rollup

**审计检查项**：
1. tenant_admin权限边界
2. 并发upsert幂等性
3. duplicate-key错误处理
4. 凭据质量聚合数据源
5. Provider Metrics数据完整性
6. readonly控制一致性

### Agent 5: IR数据结构与协议适配审计
**责任范围**：
- `internal/ir/`
- `internal/irconv/`
- OpenAI Responses API解析器
- 多协议转换

**关键提交**：
- `b76f9f2d4`: fix(ir): reject invalid Responses request shapes

**审计检查项**：
1. JSON Schema验证完整性
2. 错误消息清晰度和可诊断性
3. 防止级联失败机制
4. 其他协议解析器（OpenAI、Anthropic、Gemini）的验证一致性
5. IR字段在转换过程中的完整性
6. 序列化/反序列化对称性

### Agent 6: 流式会话与压缩审计
**责任范围**：
- `streaming/`相关文件
- 压缩provenance audit
- 请求survival预算
- L2 shadow mode

**关键提交**：
- `dbaa58a66`: feat(streaming): add zero-behavior L2 shadow mode
- `324e16566`: fix(streaming): align durable recovery with survival budget contract
- `4d27de72c`: fix(streaming): close request survival audit gaps
- `ec61336da`: fix(compression): reject stale outbound lineage

**审计检查项**：
1. L2 shadow mode隔离性
2. survival预算管理正确性
3. 持久化恢复路径完整性
4. 压缩lineage追踪
5. 并发安全（深拷贝、资源竞争）
6. 错误恢复策略

### Agent 7: 会话存储与Turn Digest审计
**责任范围**：
- `admin/session.go`
- session_bodies_hot唯一约束
- turn digest phase 2
- hot+partition架构

**关键提交**：
- `4dd6a668b`: fix(db): migration 645 repairs missing session_bodies_hot unique constraint
- `0f1a6f63e`: feat(session): turn-digest jsonb backfill job
- `d77cb92db`: feat: complete turn digest phase two routing

**审计检查项**：
1. 唯一约束修复的幂等性
2. turn digest backfill任务的并发安全
3. hot→partition迁移的数据完整性
4. digest字段在迁移中的保留
5. L0 raw cache深拷贝
6. 视图一致性

### Agent 8: 节点探测与恢复系统审计
**责任范围**：
- `bg/node_probe.go`
- 探测队列提交
- 重试和持久化
- Prometheus指标

**关键提交**：
- `476c3814e`: fix(node-probe): harden queue submission recovery
- `44bc6723d`: fix(node-probe): retry and persist queue submissions

**审计检查项**：
1. 异步探测提交正确性
2. 有界重试机制
3. 失败持久化记录
4. Prometheus指标导出
5. 探测未执行问题是否解决
6. 错误日志结构化

## 汇总分析任务

主Agent将：
1. 收集所有子Agent的审计报告
2. 识别跨模块的系统性问题
3. 生成统一的解决方案
4. 优先级排序（P0/P1/P2）
5. 生成修正计划

## 输出格式

每个Agent提供：
```json
{
  "agent_id": "Agent-X",
  "scope": "模块名称",
  "commits_reviewed": ["commit1", "commit2"],
  "findings": [
    {
      "severity": "P0/P1/P2",
      "category": "流程闭环/数据闭环/反馈闭环/安全性/可维护性",
      "title": "问题标题",
      "description": "问题描述",
      "location": "文件:行号",
      "impact": "影响范围",
      "recommendation": "修复建议"
    }
  ],
  "strengths": ["改进点1", "改进点2"],
  "test_coverage": "测试覆盖率评估"
}
```

## 时间预估
- 每个Agent审计：30-45分钟
- 汇总分析：20分钟
- 修正实施：根据问题数量
- 总时间：2-3小时
