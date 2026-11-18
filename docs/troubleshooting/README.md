# LLM Gateway 故障排查文档

本目录包含网关错误处理和故障排查的完整文档。

## 📚 文档索引

### 核心文档

1. **[错误处理指南](./error-handling-guide.md)** 
   - 错误处理架构和流程
   - 完整的ErrorKind分类体系
   - 自动路由切换机制
   - 会话诊断日志设计
   - 故障排查流程
   - 常见问题与解决方案

2. **[错误处理审计报告](./error-handling-audit-report.html)** (交互式HTML)
   - 现行方案审计结果
   - 识别的问题和严重程度
   - 完整解决方案
   - 流程图和对比表格
   - 代码改动示例

### 相关文档

- [请求流程与异常处理](../REQUEST_FLOW_WITH_EXCEPTIONS.md)
- [日志标准](../LOGGING-STANDARD.md)
- [监控指标](../metrics-catalog.md)

## 🎯 快速导航

### 🆕 最新更新（2026-11-18）

**✅ P0方案实施完成**
- 增强的错误响应结构（保留上游错误信息）
- 会话诊断日志系统（完整实现）
- 完整技术文档和迁移指南

查看：
- [P0实施总结](./P0-IMPLEMENTATION-SUMMARY.md) - 代码实现和测试结果
- [迁移指南](./error-handling-migration-guide.md) - 如何使用新功能
- [完整报告](./FINAL-REPORT.md) - 项目完整交付清单

### 我遇到错误了，怎么办？

**步骤1：获取request_id**

从客户端错误响应或响应头获取：
```json
{
  "error": {...},
  "request_id": "req-abc-123"
}
```

**步骤2：查询诊断日志**

```bash
curl "https://gateway.example.com/internal/diagnostics/request/req-abc-123"
```

**步骤3：分析失败原因**

检查返回的诊断日志：
- `attempts[].error_kind`: 错误类型
- `attempts[].next_action`: 采取的动作
- `attempts[].error.upstream_error`: 上游原始错误
- `switches`: 切换次数
- `final_error`: 最终失败原因

**详细排查流程见：** [错误处理指南 - 第五章](./error-handling-guide.md#五故障排查流程)

### 我想了解错误分类

查看 [错误处理指南 - 第二章：错误分类体系](./error-handling-guide.md#二错误分类体系)

包含20+种ErrorKind的完整说明：
- 含义和HTTP状态码
- 重试策略
- 是否记录到节点健康
- 典型场景

### 我想了解自动切换机制

查看 [错误处理指南 - 第三章：自动路由切换机制](./error-handling-guide.md#三自动路由切换机制)

包括：
- 完整的决策树
- 关键参数配置
- 节点健康状态管理

### 我想实施错误处理改进

查看 [错误处理审计报告 - 第四章：解决方案](./error-handling-audit-report.html#四解决方案)

包括：
- 增强错误响应结构（P0）
- 改进错误传递链路（P0）
- 增强可观测性（P1）
- 精细化脱敏（P1）

## 📊 诊断日志API

### 查询指定请求
```bash
GET /internal/diagnostics/request/{request_id}
```

### 查询指定会话
```bash
GET /internal/diagnostics/session/{session_id}?limit=100
```

### 查询失败请求
```bash
GET /internal/diagnostics/failures?start_time=2026-11-18T00:00:00Z&end_time=2026-11-18T23:59:59Z&limit=50
```

### 高级查询
```bash
GET /internal/diagnostics/query?tenant_id=xxx&model=gpt-4&success=false&limit=100
```

**详细API说明见：** [错误处理指南 - 第四章](./error-handling-guide.md#四会话诊断日志)

## 🛠️ 实施状态

### 已完成 ✅

- [x] 错误处理架构审计和分析
- [x] 错误分类体系文档（20+种ErrorKind）
- [x] 会话诊断日志设计和实现
  - [x] 数据结构定义 (`internal/diagnostics/session_log.go`)
  - [x] PostgreSQL存储接口 (`internal/diagnostics/logger.go`)
  - [x] HTTP API处理器 (`admin/diagnostic_handler.go`)
  - [x] 数据库迁移脚本 (`deploy/sql/migrations/0999_*.sql`)
- [x] P0核心代码实现
  - [x] 增强错误响应 (`domains/streaming/error_response_enhanced.go`)
  - [x] 集成示例 (`domains/streaming/error_response_integration_examples.go`)
  - [x] 单元测试 (`domains/streaming/error_response_enhanced_test.go`)
- [x] 完整技术文档
  - [x] 错误处理指南（6章，20KB）
  - [x] 交互式审计报告（HTML，40KB）
  - [x] 迁移指南（10.6KB）
  - [x] P0实施总结（9.7KB）
  - [x] 完整项目报告（14.5KB）

### 进行中 🚧

- [ ] 代码审查和合并
- [ ] 在handler中集成诊断日志
- [ ] 在handler中集成增强错误响应
- [ ] 运行完整测试套件
- [ ] 集成测试验证

### 计划中 📅

**P0迁移（1-2周）：**
- [ ] 迁移ExecuteError处理（最高优先级）
- [ ] 迁移路由错误处理
- [ ] 迁移鉴权错误处理
- [ ] 更新API文档

**监控部署（2周）：**
- [ ] 添加错误来源分布指标
- [ ] 监控响应时间影响
- [ ] 跟踪上游错误完整性
- [ ] 配置告警规则

**P1阶段（3-4周）：**
- [ ] routing_history字段
- [ ] 更多响应头
- [ ] 脱敏逻辑优化
- [ ] 监控仪表板

---

## 🛠️ 实施状态（原内容）

- [ ] 在handler中集成诊断日志
- [ ] 在executor中保留上游错误
- [ ] 修改writeErrorJSON系列函数
- [ ] 增强错误响应结构（添加source、upstream_error、retry_info）
- [ ] 添加响应头（X-Gateway-*系列）

### 计划中 📅

- [ ] 单元测试覆盖
- [ ] 集成测试验证
- [ ] 性能测试（日志写入不影响请求延迟）
- [ ] 监控指标接入
- [ ] 告警规则配置
- [ ] 运维手册更新

## 🔗 相关资源

### 代码文件

- 错误分类：`llm-gateway-go-integrate/errorsx/classify.go`
- 决策引擎：`llm-gateway-go-integrate/errorsx/action_policy.go`
- 路由错误：`llm-gateway-go-integrate/domains/streaming/routing_errors.go`
- 错误写入：`llm-gateway-go-integrate/domains/streaming/handler.go`
- 诊断日志：`llm-gateway-go-integrate/internal/diagnostics/`

### 数据库

- 迁移脚本：`deploy/sql/migrations/0999_add_request_diagnostic_logs.sql`
- 视图：`v_diagnostic_failure_summary`, `v_diagnostic_failover_summary`

### API

- 路由注册：`admin/diagnostic_handler.go`
- API文档：见错误处理指南第四章

## 📝 更新日志

### 2026-11-18

- ✅ 完成错误处理架构审计
- ✅ 设计会话诊断日志方案
- ✅ 实现诊断日志存储接口
- ✅ 创建数据库迁移脚本
- ✅ 实现HTTP API处理器
- ✅ 编写完整技术文档

---

**维护团队：** Platform Team  
**文档更新：** 2026-11-18  
**问题反馈：** [提交Issue](https://github.com/yourorg/llm-gateway/issues)
