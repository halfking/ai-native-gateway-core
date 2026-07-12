# 项目配置信息

> **重要提示**: 此文档包含敏感信息，请勿提交到公共仓库。智能体在每次会话开始时应首先读取本文件。

## 项目概述

**项目名称**: LLM Gateway  
**项目类型**: AI模型统一网关服务  
**技术栈**: Go (后端) + Vue 3 (前端) + PostgreSQL (数据库)  
**部署方式**: systemd service + Nginx反向代理  

## 环境架构

### 生产环境

```yaml
域名配置:
  主域名: llmgo.kxpms.cn
  域名模式: *.kxpms.cn (火山服务统一域名)

服务器信息:
  SSH地址: 14.103.112.184:25022
  默认用户: admin
  默认密码: Veritrans&9527
  Root密码: Kaixuan2026&#*9527
  
数据库配置:
  类型: PostgreSQL
  SSH隧道: 14.103.112.184:25022
  本地地址: 127.0.0.1:5432
  数据库名: llm_gateway
  用户名: postgres
  访问方式: 通过SSH隧道连接
```

### 开发环境

```bash
# 本地开发路径
工作目录: /Users/xutaohuang/workspace/official-deploy/services/llm-gateway-go

# Go环境要求
Go版本: 1.21+
包管理: go mod

# 前端环境要求
Node版本: 16+
包管理: npm/yarn
```

## API配置

### 用户API端点

```yaml
基础URL: https://llmgo.kxpms.cn/v1

端点列表:
  - POST /v1/chat/completions       # OpenAI兼容的对话接口
  - GET  /v1/models                 # 模型列表
  
认证方式:
  类型: Bearer Token
  Header: Authorization: Bearer sk-{YOUR_API_KEY}
  
测试密钥:
  示例: sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9
```

### Admin API端点

```yaml
基础URL: https://llmgo.kxpms.cn/api

端点列表:
  - GET  /api/logs                  # 请求日志查询
  - GET  /api/stats                 # 统计信息
  - GET  /api/credentials           # 凭证管理
  
认证方式:
  类型: Bearer Token
  Header: Authorization: Bearer {LLM_GATEWAY_ADMIN_API_KEY}
  环境变量: LLM_GATEWAY_ADMIN_API_KEY
  注意: Admin API使用独立的认证密钥，不能使用用户API Key
```

## 代码库结构

```
llm-gateway-go/
├── cmd/                      # 应用程序入口
│   └── server/              # 主服务入口
├── domains/                  # 领域模型（DDD架构）
│   ├── credentials/         # 凭证管理域
│   ├── models/              # 模型管理域
│   ├── requests/            # 请求处理域
│   └── remotecontrol/       # 远程控制域（新增）
├── handlers/                 # HTTP处理器
│   ├── v1/                  # 用户API v1
│   └── admin/               # 管理员API
├── infrastructure/           # 基础设施层
│   ├── database/            # 数据库连接
│   ├── config/              # 配置管理
│   └── middleware/          # 中间件
├── migrations/               # 数据库迁移文件
├── web-admin/               # 前端管理界面
│   ├── src/
│   │   ├── components/     # Vue组件
│   │   ├── views/          # 页面视图
│   │   ├── locales/        # 国际化文件（8种语言）
│   │   └── router/         # 路由配置
│   └── dist/               # 构建产物
└── docs/                    # 项目文档
```

## 开发规范快速参考

### Go代码规范

```go
// 命名规范
type UserRequest struct {          // 类型：大驼峰
    ClientModel string             // 导出字段：大驼峰
    requestID   string             // 私有字段：小驼峰
}

// 数据库列名：snake_case
// SQL: client_model, request_id

// 错误处理
if err != nil {
    log.Printf("failed to ...: %v", err)
    return fmt.Errorf("...: %w", err)
}

// 日志规范
log.Printf("[INFO] ...")   // 信息
log.Printf("[WARN] ...")   // 警告
log.Printf("[ERROR] ...")  // 错误
```

### 数据库迁移规范

```bash
# 创建迁移文件
migrate create -ext sql -dir migrations -seq descriptive_name

# 文件命名格式
000001_descriptive_name.up.sql    # 升级脚本
000001_descriptive_name.down.sql  # 回滚脚本

# 列命名规范
使用 snake_case：user_id, created_at, request_mode
避免使用保留字：model → client_model
```

### Git提交规范

```bash
# Commit Message格式
<type>(<scope>): <subject>

# Type类型
feat:     新功能
fix:      Bug修复
refactor: 重构
chore:    构建/工具变更
docs:     文档更新

# 示例
fix(streaming/telemetry): stop silent request_logs drop on schema mismatch
feat(admin): add attachment display in request log drawer
```

## 常用命令

### 后端开发

```bash
# 启动开发服务器
go run cmd/server/main.go

# 运行测试
go test ./...

# 构建二进制
go build -o llm-gateway-go cmd/server/main.go

# 数据库迁移
migrate -path migrations -database "postgres://..." up
migrate -path migrations -database "postgres://..." down
```

### 前端开发

```bash
# 进入前端目录
cd web-admin

# 安装依赖
npm install

# 开发模式
npm run dev

# 构建生产版本
npm run build

# 添加国际化文本（需要同时更新8种语言）
# 文件位置: src/locales/{en,zh-CN,ja,ko,es,fr,de,ru}.ts
```

### 服务器部署

```bash
# SSH连接
ssh -p 25022 admin@14.103.112.184

# 切换到root
sudo su -
# 密码: Kaixuan2026&#*9527

# 查看服务状态
systemctl status llm-gateway

# 重启服务
systemctl restart llm-gateway

# 查看日志
journalctl -u llm-gateway -f
```

### 数据库操作

```bash
# 通过SSH连接数据库
ssh -p 25022 -L 5432:127.0.0.1:5432 admin@14.103.112.184

# 在本地连接
psql -h 127.0.0.1 -p 5432 -U postgres -d llm_gateway

# 常用查询
# 查看最近的请求日志
SELECT * FROM request_logs ORDER BY created_at DESC LIMIT 20;

# 查看特定模型的请求
SELECT * FROM request_logs WHERE client_model = 'claude-sonnet-4-6' ORDER BY created_at DESC;

# 统计成功率
SELECT 
    client_model,
    COUNT(*) as total,
    SUM(CASE WHEN success THEN 1 ELSE 0 END) as success_count,
    ROUND(100.0 * SUM(CASE WHEN success THEN 1 ELSE 0 END) / COUNT(*), 2) as success_rate
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY client_model;
```

## 测试指南

### API测试

```bash
# 测试用户API（非流式）
curl https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-YOUR_API_KEY" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": false
  }'

# 测试用户API（流式）
curl https://llmgo.kxpms.cn/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-YOUR_API_KEY" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'

# 测试Admin API
curl https://llmgo.kxpms.cn/api/logs \
  -H "Authorization: Bearer ${LLM_GATEWAY_ADMIN_API_KEY}"
```

### 测试报告规范

所有测试结果应保存到 `docs/` 目录，使用以下命名格式：

```
docs/{FEATURE}_TEST_REPORT.md
docs/{MODEL_NAME}_TEST_REPORT.md
docs/{BUG_ID}_FIX_VERIFICATION.md
```

测试报告应包含：
1. 测试目标和范围
2. 测试环境信息
3. 测试步骤和结果
4. 数据库验证结果
5. 结论和建议

## 故障排查

### 常见问题

#### 1. Admin API返回500错误

**症状**: 访问 /api/logs 返回500  
**原因**: 数据库列名不匹配  
**检查**: 查看错误日志中的SQL语句，确认列名是否使用snake_case  
**解决**: 检查Go代码中的结构体tag，确保与数据库列名一致  

#### 2. 请求日志未记录

**症状**: 数据库中找不到请求记录  
**原因**: Schema不匹配导致静默丢弃  
**检查**: 查看应用日志，搜索 "schema mismatch" 或 "failed to insert"  
**解决**: 更新数据库schema或修改代码中的结构体定义  

#### 3. 流式响应中断

**症状**: SSE流式响应提前终止  
**原因**: 可能是超时、网络问题或上游服务错误  
**检查**: 查看 stream_chunk_count 和 latency_ms  
**解决**: 增加超时时间，检查上游服务状态  

### 日志位置

```bash
# 应用日志
journalctl -u llm-gateway -f

# Nginx访问日志
/var/log/nginx/access.log

# Nginx错误日志
/var/log/nginx/error.log

# PostgreSQL日志
/var/log/postgresql/postgresql-*.log
```

### 调试技巧

```bash
# 开启Go详细日志
export LOG_LEVEL=debug

# 查看实时数据库连接
psql -c "SELECT * FROM pg_stat_activity WHERE datname = 'llm_gateway';"

# 监控请求实时写入
psql -c "SELECT COUNT(*) FROM request_logs;" --watch 1

# 抓包分析
tcpdump -i any port 5432 -w db.pcap
```

## 智能体使用指南

### 会话开始时必做

1. ✅ 读取本文件 (PROJECT_CONFIG.md)
2. ✅ 读取 DEVELOPMENT_STANDARDS.md
3. ✅ 读取 SESSION_RESUME.md 了解当前状态
4. ✅ 确认任务目标和验收标准

### 实施变更时必做

1. ✅ 变更前先读取相关文件
2. ✅ 遵守开发规范
3. ✅ 变更后运行测试验证
4. ✅ 将结果记录到docs/目录

### 遇到错误时必做

1. ✅ 记录完整的错误信息和上下文
2. ✅ 查阅本文件的故障排查章节
3. ✅ 尝试多种解决方案，记录尝试过程
4. ✅ 解决后更新文档，避免重复踩坑

### 上下文压缩后

1. ✅ 立即读取 SESSION_RESUME.md
2. ✅ 检查 docs/ 目录下的最新报告
3. ✅ 确认当前Git状态
4. ✅ 继续未完成的工作

## 更新日志

| 日期 | 更新内容 | 更新人 |
|------|---------|--------|
| 2026-07-02 | 初始版本，整合环境、API、规范信息 | AI Agent |

---

**最后更新**: 2026-07-02  
**文档状态**: 🟢 Active  
**下次审计**: 2026-07-09  
