# llm-gateway-go

LLM Gateway Go 数据面 — 高性能身份感知 LLM 请求代理网关

## 项目概述

Go 数据平面 for the LLM gateway，提供身份感知路由、流量控制、请求标准化与审计追踪。

## 技术栈

- **语言**: Go 1.21+
- **框架**: 原生 HTTP (net/http)
- **数据库**: PostgreSQL (ent ORM)
- **前端**: Vue 3 + Vite + TypeScript

## 目录结构

```
llm-gateway-go/
├── cmd/                    # 程序入口
├── relay/                  # HTTP 请求中继核心
│   ├── handler.go          # 请求处理与路由解析
│   ├── stream.go           # SSE 流式响应处理
│   ├── chat.go             # Chat Completion 处理
│   └── normalize.go        # 请求/响应标准化
├── routing/                # 路由与执行
│   ├── executor.go        #候选执行、重试、粘性路由
│   ├── router.go           # 路由逻辑
│   └── sticky.go           # 粘性会话管理
├── circuit/                # 熔断器
│   └── breaker.go          # 凭证级熔断状态机
├── limiter/                # 并发限流
│   └── pool/              # 身份绑定连接池
├── pool/                   # 连接池管理
├── provider/               # 提供商解析
│   └── client.go          # 提供商/策略解析
├── identity/               # 身份管理
│   └── identity.go        # 身份哈希与虚拟地址推导
├── transform/              # 请求转换
│   └── transform.go       # 请求转换与模型渲染
├── upstream/              # 上游调用
│   └── client.go          # 上游 LLM 客户端
├── telemetry/             # 遥测
│   └── client.go          # 审计与遥测事件
├── audit/                  # 审计
│   └── audit.go           # 审计日志
├── sessions/              # 会话管理
│   ├── handler.go         # 会话中间件
│   ├── middleware.go      # 会话中间件
│   └── cache_injector.go # 缓存注入
├── credentialstate/       # 凭证状态
├── disguise/              # 请求伪装
├── secret/                # 密钥管理
│   └── fernet.go         # Fernet 对称加密
├── ratelimit/             # 限流
│   └── sliding.go        # 滑动窗口限流
├── bg/                     # 后台任务
│   ├── credential_cycler.go  # 凭证轮换
│   ├── credential_recovery.go # 凭证恢复
│   ├── sticky_cleaner.go     # 粘性清理
│   └── taxonomy_sync.go      # 分类同步
├── admin/                  # 管理接口
│   ├── usage.go          # 用量管理
│   └── apply.go          # 配置应用
├── middleware/            # HTTP 中间件
├── modelname/             # 模型名标准化
│   └── normalize.go      # 模型名规范化
├── web/                   # Vue 3 管理后台
│   └── src/
│       ├── views/        # 页面视图
│       ├── components/   # 组件
│       ├── api.ts       # API 客户端
│       └── router.ts    # 路由配置
├── ent/                    # 数据库实体模型
├── db/                     # 数据库连接
├── build/                  # 构建输出
└── gateway                 # 编译后的二进制
```

## 主要模块

### 核心处理流程

```
请求入口 → relay/handler.go → routing/executor.go → circuit/breaker.go → upstream/client.go → 响应
              ↓                    ↓                    ↓
         路由解析              候选执行              熔断/限流
```

### 关键模块说明

| 模块 | 文件 | 职责 |
|------|------|------|
| 请求中继 | `relay/handler.go` | HTTP 请求生命周期和降级路由 |
| 路由执行 | `routing/executor.go` | 候选规划、重试、状态写入、粘性路由 |
| 流式处理 | `relay/stream.go` | SSE 流式代理、超时、保活 |
| 熔断器 | `circuit/breaker.go` | 凭证级熔断状态机 |
| 连接池 | `pool/pool.go` | 身份绑定连接池 |
| 身份管理 | `identity/identity.go` | 身份哈希和虚拟地址推导 |
| 请求转换 | `transform/transform.go` | 请求转换和出站模型渲染 |
| 限流器 | `limiter/limiter.go` | 并发限制 |
| 滑动窗口 | `ratelimit/sliding.go` | 滑动窗口限流 |

### 错误处理

七类错误处理：
- **TRANSIENT**: 5xx 临时错误 → 冷却 60s 后恢复
- **TIMEOUT**: 连接/读取超时 → 退避重试
- **NETWORK**: DNS/连接拒绝 → 退避重试
- **RATE_LIMIT**: 429 → 指数退避 30s→60s→120s...
- **AUTH**: 401/403 → 永久隔离 (quarantine)
- **QUOTA**: 余额不足 → 永久隔离
- **UPSTREAM_DOWN**: 502/503/504 → 指数退避

## 入口文件

- **主程序**: `cmd/` 目录（未显示，需查看 cmd/gateway/main.go）
- **管理后台**: `web/` (Vue 3 + Vite, 端口由配置指定)

## 运行时端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/healthz` | GET | 健康检查 |
| `/v1/chat/completions` | POST | Chat Completion API |
| `/v1/completions` | POST | Completion API |
| `/v1/messages` | POST | Messages API |
| `/v1/responses` | POST | Responses API |
| `/v1/models` | GET | 模型列表 |

## 验证

```bash
go test ./...
gofmt -w ./...
```

## 依赖关系

```
relay/handler.go
  ├── routing/executor.go
  ├── circuit/breaker.go
  ├── limiter/limiter.go
  └── transform/transform.go
        ├── identity/identity.go
        └── modelname/normalize.go
```

## 备注

- Python 控制面是供应商和策略管理的真实数据源
- Go 数据面负责高性能请求转发
- 身份隧道确保多用户场景下的请求隔离
- 支持 OpenAI-compatible 和 Anthropic-compatible 请求格式