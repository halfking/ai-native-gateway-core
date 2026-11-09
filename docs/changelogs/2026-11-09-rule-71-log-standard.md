# 2026-11-09 接入 rule 71 统一日志标准（补记）

> 本文补记 rule 71 首版接入——该次接入当时漏记了 `CHANGELOG.md` 与
> `docs/changelogs/` 归档（违反 rule 36），于 2026-11-09 补齐。
> 补强轮次见 `2026-11-09-rule-71-reserved-names-and-early-install.md`。

规范正文：`vibe-coding/rules/71-log-standard.md`；字段契约 SSOT：
`vibe-coding/spec/logging/log-schema.yaml`。

## 做了什么

- 新增 `internal/logging/standard.go`：规范字段注入 + handler 层脱敏。
  - 9 个必填字段自动注入：`timestamp`（RFC3339 UTC 纳秒）/ `severity_text`（大写）
    / `severity_number`（OTel 数值表）/ `level`（小写别名）/ `msg` /
    `service` / `version` / `env` / `instance_id`。
  - 扁平单层输出（不引入 `slog.Group` 嵌套）。
  - 脱敏在 **handler 层**做（调用点不会漏）：敏感键命中 → `[REDACTED]`；
    值命中敏感模式（Bearer / JWT / AKIA / `sk-` / 长十六进制）→ 整体替换。
- 新增 `internal/logging/service_meta.go`：服务名 / 版本 / 环境解析，
  版本由构建期注入，不硬编码。
- `internal/logging/logging.go`：`installStandard` 作为**唯一**的 handler
  安装入口（`Init` / `ReInit` / `Reconfigure` 全部走它）——否则运行期切换
  日志配置后格式会退回旧版。

## 为什么 `level` 与 `severity_text` 都要

`severity_text` 用**大写**（OTel 原生形态），`level` 用**小写**（ECS `log.level`
惯例），两者是**独立字段**而非别名。

原因：下游 `tools/log-manager/analyzer` 用 `level=~"error|fatal"` 过滤错误日志。
若 `level` 是大写，这条选择器会**静默漏掉全部错误日志**——不报错，只是没数据。
双名并存各取所长，OTel 风格与 ECS 风格的查询都能命中同一行。

## 保留名优先

`service` / `version` / `env` / `instance_id` / `level` / `severity_text` /
`severity_number` 由规范层独占写入，业务字段不得占用。

需要表达别的意思时改用无歧义键名：环境变量名 → `env_var`；另一个实体的
实例 ID → `agent_instance_id`；插件版本 → `plugin_version`。

## 验证

`go build ./... && go vet ./...` 通过；`go test ./internal/logging/... ./cmd/...`
通过。端到端用容器真实环境变量启动新二进制采样，stderr 12/12 行、
主日志文件 11/11 行、persistent `shutdown.log` 1/1 行全部通过
`vibe-coding/scripts/verify-log-standard.py` 校验。

## 没有做成的事

- 本地容器 `llm-gateway-local-8782` **当时仍在跑旧镜像**（旧格式：字段名
  `time`、大写 `level`、无必填字段），重建容器需要明确确认，本轮未执行。
- 其余同名克隆仓（`llm-gateway-go-3`）已同源同步，本仓之外的存量服务
  按台账排期分批改造。
