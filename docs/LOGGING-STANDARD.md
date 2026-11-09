# 日志规范落地说明（rule 71）

> **生效日期**：2026-11-09
> **状态**：Active
> **依据规范**：`vibe-coding/rules/71-log-standard.md`（统一日志标准）
> **字段契约 SSOT**：`vibe-coding/spec/logging/log-schema.yaml`
> **机器门禁**：`bash ~/workspace/ai-native-tools/vibe-coding/scripts/verify.sh log-standard --service llm-gateway`

---

## 1. 改造前的问题

| 问题 | 实测证据 | 后果 |
|------|---------|------|
| **同一服务两种日志格式** | `cmd/gateway`、`cmd/quality-service`、`cmd/license-authority` 用 JSONHandler；`cmd/mcp-gateway`、`cmd/gateway-v2`、`cmd/regen-credentials`、`cmd/llm-gw-annotator`、`cmd/llm-gw-exporter` 用 **TextHandler** | AI 消费同一服务的日志要写两套解析分支 |
| **缺必填字段** | 不输出 `service` / `version` / `env` / `instance_id` | 日志无法归属到服务与版本，跨服务归因断裂 |
| **无脱敏** | handler 层无 redact | 密钥/令牌泄漏风险 |
| **服务标识分散** | 每个 cmd 自己决定叫什么，没有统一来源 | 同一服务的两个入口可能叫不同名字，AI 聚合会分裂 |

---

## 2. 改造后的形态

### 2.1 输出

```json
{"timestamp":"2026-11-09T10:15:41.181Z","severity_text":"INFO","level":"INFO","severity_number":9,"msg":"gateway starting","service":"llm-gateway","version":"2.4.1","env":"prod","instance_id":"kbz-4213-1794219341"}
```

- **扁平 JSONL**，一行一记录
- **8 个必填字段**由 handler 层自动注入，调用点无需改
- `severity_text`（OTel 原生名）与 `level`（ECS 别名）**同时输出**

### 2.2 代码分层

```
internal/logging/
├── logging.go       既有：写到哪、怎么轮转（lumberjack + Bleve fanout）
├── standard.go      新增：写什么、长什么样（规范字段 + 脱敏）
└── service_meta.go  新增：service / version / env 的统一来源
```

三者**正交组合**，不互相替代。`logging.Init` 内部接入规范层——**所有既有调用方自动获益**，无需改调用点。

### 2.3 统一入口

| 场景 | 用法 |
|------|------|
| 主网关（走 `logging.Init`） | 自动获得规范字段，无需改动 |
| 其他 cmd 入口 | `logging.NewServiceLogger(slog.LevelInfo)` |
| 需要 stderr 输出 | `logging.NewServiceStderrLogger(slog.LevelInfo)` |

---

## 3. 服务元信息来源

| 字段 | 来源 | 说明 |
|------|------|------|
| `service` | `LLM_GATEWAY_SERVICE` 环境变量，默认 `llm-gateway` | 必须与 `deploy/platform/services.json` 的服务键一致 |
| `version` | `LLM_GATEWAY_VERSION` > `version.json` > `dev` | 与 `cmd/gateway/version.go` 的 SSOT 同源 |
| `env` | `LLM_GATEWAY_ENV`（兼容 `GO_ENV`） | **归一**到 `local`/`dev`/`test`/`staging`/`prod` |

> **`env` 归一的原因**：本仓历史上用 `production` 表示生产环境。
> 不归一会让 AI 按 env 分组时出现 `prod` 与 `production` 两个桶。
> 归一逻辑见 `internal/logging/service_meta.go` 的 `ServiceEnv()`。

---

## 4. 运维影响

### 4.1 日志格式变更（⚠️ 破坏性）

5 个 cmd 入口的日志从纯文本变为 JSONL。依赖文本匹配的运维脚本需改写：

```bash
# 旧
grep 'ERROR' logs/gateway.log

# 新
grep '"severity_text":"ERROR"' logs/gateway.log
# 或用 jq
jq -r 'select(.severity_text=="ERROR") | .msg' logs/gateway.log
```

主网关（`cmd/gateway`）此前已是 JSON，字段名有变化：`time` → `timestamp`，`level` → `severity_text` + `level`。

### 4.2 Bleve 全文检索

`/admin/logs/search` 的索引内容随之变化（索引的是规范字段后的 JSON）。
`internal/logging/bleve_fanout.go` 里提升到顶层的关联字段白名单不变，
但 `msg`/`level` 的键名已按规范调整。

### 4.3 脱敏生效范围

`password` / `secret` / `token` / `api_key` / `authorization` / `cookie` / `session_id` /
`credential` / `handoff_token` 等键，以及 Bearer / JWT / AWS AKIA / `sk-` / 长十六进制等值模式，
**在 handler 层**统一脱敏。

> 脱敏是**最后一道防线**，不是许可证——仍不要把 secret 传给 logger。

---

## 5. 验收

```bash
# 构建
go build ./...

# 单测
go test ./internal/logging/... ./middleware/... ./cmd/gateway/... ./cmd/mcp-gateway/...

# 门禁抽样校验真实输出
bash ~/workspace/ai-native-tools/vibe-coding/scripts/verify.sh log-standard \
  --log-file /path/to/gateway.log
```

---

## 6. 跨服务一致性

本改造后，本仓与 `agent-companion` 产出**同名字段**，
AI 日志分析器（`tools/log-manager/analyzer`）可用同一套 LogQL 与聚类逻辑跨服务聚合，
无需为每个服务写适配层。
