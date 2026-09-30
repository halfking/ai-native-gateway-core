# 配置完备参考（CONFIG-REFERENCE）

本文档是 `envs.samples/` 目录的**总说明**，与两份配套物一起构成完整配置面：

- `01-gateway-core.env.sample` ~ `10-installer.env.sample` —— 按场景的可执行示例配置；
- `SPECS-REFERENCE.md` —— **机器生成**的 settings 规格全表（104 个变量，
  由 `go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md` 再生，
  数据源与线上热加载通道同源，勿手改）。

覆盖口径（2026-10-01 审计）：Go 代码直接读取（`os.Getenv`/`os.LookupEnv`）、
辅助函数读取（`envOrDefault`/`getEnvBool`/`parseIntEnv` 等）、结构体
`env:"..."` 标签、settings 规格 `EnvName`、docker-compose 插值、部署与
运维脚本，共 600+ 变量；全部在本目录中有归属（运行时入示例文件、
测试与工具型入 §7/§8 清单）。

## 1. 配置加载与优先级

```
settings_kv 数据库值（热加载，仅 settings 规格族）
  > 环境变量 / .env 文件
    > YAML 配置（LLM_GATEWAY_CONFIG_FILE 指定，默认搜索路径见 config/config.go）
      > 代码内默认值
```

- 主加载链在 `config/config.go`（结构体 `env:"..."` 标签 + `mergeFromYAML`）；
- settings 规格族（见 SPECS-REFERENCE.md）额外支持 admin 后台按
  platform/tenant 作用域热覆盖；
- `.env` 文件本身不被 Go 进程解析，由部署层（compose `env_file`、
  systemd `EnvironmentFile`、`source`）注入为进程环境变量。

## 2. 最小启动集（容器栈必填五项 + 推荐）

`docker-compose.yml` 用 `:?` 强校验以下五项，缺失直接拒绝启动：

| 变量 | 说明 |
| --- | --- |
| `LLM_GATEWAY_DATABASE_URL` | PostgreSQL DSN（fallback `DATABASE_URL`） |
| `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` | 凭据加密 key（base64url 32B，**设后绝不可换**） |
| `LLM_GATEWAY_SECRET_KEY` | 会话签名 key（fallback `SECRET_KEY`；换则全员会话失效） |
| `LLM_GATEWAY_API_KEY` | 数据面 Bearer token |
| `LLM_GATEWAY_ADMIN_API_KEY` | 控制面 /api/admin/* token |

裸跑最小集：上述五项 + `LLM_GATEWAY_REDIS_ADDR`（如需 Redis 特性）。

## 3. 前缀 / 无前缀 fallback 对照

同一配置存在两个名字时，**前缀版优先**，无前缀版仅作兼容 fallback
（`firstNonEmpty` 链，config/config.go）：

| 规范名（优先） | 兼容 fallback | 备注 |
| --- | --- | --- |
| `LLM_GATEWAY_DATABASE_URL` | `DATABASE_URL` | 部署脚本两侧互补 |
| `LLM_GATEWAY_SECRET_KEY` | `SECRET_KEY` | quickstart compose 用无前缀名 |
| `LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY` | `CREDENTIAL_ENCRYPTION_KEY` | 同上 |
| `LLM_GATEWAY_ENV` | `GO_ENV` → `APP_ENV` | 部署环境标识 |
| `LLM_GATEWAY_WECHAT_CORP_ID` / `_CORP_SECRET` | `WECHAT_CORP_ID` / `WECHAT_CORP_SECRET` | 企微通知 |
| `LLM_GATEWAY_ANALYSIS_BASE_URL` / `_API_KEY` | `LLM_ANALYSIS_BASE_URL` / `_API_KEY` | 会话分析模型（旧名） |
| `LLM_GATEWAY_JWT_SECRET` | （回落 `SECRET_KEY`） | 未设时用 SECRET_KEY 签 JWT |
| `LLM_GATEWAY_MAINTAIN_URL` | `MAINTAIN_SERVICE_URL` | maintain 服务地址 |
| `LLM_GATEWAY_REDIS_URL` / `REDIS_URL` | — | URL 形式；`URSM_V2_*` 族**天生无前缀**（见 02 注） |

## 4. 核心运行时变量（对应 01-gateway-core.env.sample）

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `LLM_GATEWAY_LISTEN` | `:8781` | 监听地址（config.go:582） |
| `LLM_GATEWAY_ENV` | — | dev/staging/production 标识 |
| `LLM_GATEWAY_RUNTIME_ROLE` | `active` | `active` \| `traffic-only`；蓝绿 worker 归属，非法值 fail-closed（config/runtime_role.go） |
| `LLM_GATEWAY_PPROF_LISTEN` | 空 | pprof 端口，留空关闭 |
| `LLM_GATEWAY_DB_MAX_CONNS` | 代码默认 | PG 连接池上限 |
| `LLM_GATEWAY_STORAGE_MAX_CONNECTIONS` | 代码默认 | 存储层独立池上限 |
| `LLM_GATEWAY_REDIS_ADDR/_PASSWORD/_DB` | — / — / `2` | session/live-stream/rpm/availability 共用；共享实例 db=2（0/1/9 被占） |
| `RATE_LIMIT_REDIS_URL` / `RPM_REDIS_URL` | 回落主 Redis | 限流 / RPM 统计专用通道 |
| `LLM_GATEWAY_SESSION_SERVICE_JWT_SECRET/_ISSUER/_AUDIENCE/_ENABLED` | — | session-manager 服务间 JWT 门禁 |
| `CURSOR_HMAC_SECRET` | — | Cursor 客户端 HMAC 校验 |
| `LLM_GATEWAY_IDENTITY_SALT` | — | 客户端指纹盐 |
| `LLM_GATEWAY_ADMIN_USER/_PASSWORD/_EMAIL` | — | 引导管理员（`_SEED_ADMIN_PASSWORD` 为首次 seed 形式） |
| `LLM_GATEWAY_ENDPOINT` / `_GATEWAY_BASE_URL` | 自动推导 | 对外可达地址（注册上报/自检） |
| `LLM_GATEWAY_LAN_ADVERTISE` | — | mDNS 局域网广播 |
| `LLM_GATEWAY_BEHIND_TLS` / `_COOKIE_SECURE` | `false` | 前置 TLS 反代时置 true |
| `LLM_GATEWAY_CORS_ORIGINS` | — | 逗号分隔来源，`*` 全放 |
| `LLM_GATEWAY_TRUSTED_PROXY_CIDRS` | 内置默认表 | 真实 IP 信任代理网段 |
| `LLM_GATEWAY_UPSTREAM` | `http://127.0.0.1:8780` | 单上游直通模式（多供应商走 DB 配置） |
| `LLM_GATEWAY_UPSTREAM_TIMEOUT` 等 8 个超时 | 见 config.go | upstream/stream/chunk/first-byte/response-header/keepalive/sync-retry/SSE 行上限 |
| `LLM_GATEWAY_SESSION_TTL_HOURS` / `_SESSION_REUSE_WINDOW` / `_SESSION_ID_BODY_KEYS` / `_DEFAULT_LANGUAGE` | — | 会话基础 |
| `LLM_GATEWAY_LOG_LEVEL/_FORMAT/_FILE/_MAX_*` | info/json/空/100×10×7 | 留空走 stderr；填路径启用轮转 |
| `LLM_GATEWAY_DATA_DIR/_BODIES_DIR/_BODIES_CODEC/_BACKUP_DIR` | ./data 相对 / gzip | 落盘布局；回滚旧二进制前 codec 切回 gzip |
| `LLM_GATEWAY_STORAGE_MODE/_TYPE/_SQLITE_PATH` | db / postgres / — | 单机 sqlite 模式入口 |
| `LLM_GATEWAY_ATTACHMENT_*`（10 项） | — | 附件目录/认证模式/大小上限/出站抓取等，见 01 |
| `LLM_GATEWAY_JWT_EXPIRY` / `_AUTH_STALE_GRACE_SECONDS` | — | admin JWT 时效 / 认证过期宽限 |
| `LLM_GATEWAY_BG_MODE` | `full` | `data-plane` 跳过 Python 71 拥有的后台循环 |
| `LLM_GATEWAY_IR_CONVERTER` | 开 | false 仅用于紧急回滚 legacy 协议转换路径 |
| `LLM_GATEWAY_POOL_GRACE_PERIOD` | — | 连接池优雅释放宽限（秒） |
| `LLM_GATEWAY_LOG_DIR/_LOGS_DIR/_LOG_INDEX_DIR/_CACHE_DIR` | 相对工作目录 | 目录布局补充 |
| `LLM_GATEWAY_KEYSTORE_SNAPSHOT_DIR/_SYNC_INTERVAL` | — | keyring 密钥轮转快照/同步 |
| `LLM_GATEWAY_V2_ENABLED` / `_SESSIONS_V2_COMPRESSION_READ` / `SESSIONS_V2_PRIMARY_READ` | — / 1 / 0 | sessions v2 开关族（模式切换用 `URSM_V2_MODE`） |
| `LLM_GATEWAY_LITE_ALLOW_UNAUTHENTICATED` | `false` | lite 会话体免鉴权，仅本地调试 |
| `LLM_GATEWAY_STATIC_DIR` / `_PYTHON_ENDPOINT` | 内嵌 / 空 | admin 静态资源 / Python 辅助服务 |

## 5. settings 规格族（104 项，见 SPECS-REFERENCE.md）

压缩、伪装、会话、限流、透传、模块、日志、存储、会话审计、探测、自检、
错误探测、路由状态、生命周期、会话分析、看板、统计影子、Sessions V2、
session-service JWT、供应商画像、自动摘要、模型质量、per-turn digest、
V2 调度、项目归属、网关准入、代理、阈值表、节点故障转移、敏感词、
峰谷倍率、报表聚合、Goal、Handoff、AutoControl。

每一项的默认值/类型/范围/中文说明以 `SPECS-REFERENCE.md` 为准；
改 specs 代码后重新生成即可同步文档。

## 6. 非 settings 功能开关（对应 02-gateway-features.env.sample）

按家族索引（逐项注释见 02 文件本身）：

| 家族 | 读取点 | 要点 |
| --- | --- | --- |
| 存活恢复 holdback / L2 / survival worker | internal/probe、bg | 默认关；HOLDBACK 示例 5000ms×20 chunks |
| 流重试 / 空流门 / pre-stream keepalive | config.go、internal/streamretry | 默认开；阈值与退避可调 |
| reqprobe 参数账本 | internal/reqprobe | `off` 关闭学习 |
| Extensions 透传（IR） | internal/ir | `LLM_GATEWAY_TRANSPORT_IR=true` 全量透传 |
| Goal / Handoff 扩展旋钮 | cmd/gateway/goal_control.go、settings/*_specs.go | 段内 35+ 项；cost_mode 三级预设 |
| 托管任务（hosted tasks） | config.go:333-350 | 启用前须完成跨仓库前置门禁 |
| 凭据健康（fp slot/nudge/auto-revoke/恢复） | credentialhealth 等 | 恢复 worker 在 traffic-only 也必跑 |
| 配额/余额巡检、供应商画像、成本对账 | bg | 默认关，间隔类可调 |
| Mock Probe | internal/mockprobe | 默认全关；interval<1s 回落 30s |
| MNF 连败冷却 | settings/spec_thresholds.go | 见 SPECS 表 |
| 压缩/compaction 家族 | settings/spec_compression.go + 内部 | 大部分入 SPECS 表 |
| URSM v2 | domains/ursm/v2/config.go | **无前缀**；off/shadow/canary/authoritative |
| 保留窗口 / vacuum | URSM_SNAPSHOT_RETENTION_DAYS 等 | 默认 30d / 7d |
| Hotzone / 请求归档 / 原始日志 / 完整性采集 | internal/requestarchive 等 | 默认关 |
| 调度 / pending / 幂等 | dispatch 族 | 队列后端与 governor 可换 |
| 路由评分权重 / 压力感知曲线 | routing w_* / pressure α·β·knee·γ | 多目标打分与压力拐点 |
| ML 路由（onnx） | settings/routing_ml_flags.go | ROUTING_ML_* 六项 |
| V2 子系统门禁 | V2_AUTH/CACHE/STREAMING/AUDIT/... | 迁移期灰度开关族 |
| 粘性负载 / live-stream 缓存 / 探测队列 / 提交门 | stickyload、probe 族 | 容量与窗口类 |
| 资源自愈监控 / 启动重试 / 身份限流器 | resource monitor、boot retry | 阈值与窗口 |
| JEV 分类 / 提示缓存 / 遥测兜底 | 内部 | 观察类 |
| 对象存储后端（S3/OSS/Cloudreve） | storage | 三套凭据，附件/备份外置 |
| 会话分析（SA）引擎 | settings/session_analytics_specs.go | 模型 alias 与聚类调度 |
| Bleve 日志全文索引 | internal/logging/bleve_fanout.go | 默认关 |
| 数据生命周期 cron | admin/data_lifecycle_cron_env.go | HOT_CRON_* 六项 |
| 路由优化插件 | settings/routing_opt_feature_flags.go | 默认关，零开销短路 |
| Autoroute 特性开关 | autoroute/feature_flags.go | AUTO_* 24 项 |
| 路由/故障/探测阈值 | settings/spec_thresholds.go | Wave2 集中化常量表 |
| 通知杂项 | 见 04 | 钉钉/飞书/企微/候选失败告警 |
| 集成 | 见 03 | Casdoor/Memora/ASM/maintain/ACC/Armor/License |
| GeoIP / 代理订阅 / 出口标识 | admin、proxy | 可选覆盖 |

## 7. 测试专用变量（不进运行时 env）

主入口约定（详见 `09-testing.env.sample`）：`TEST_DATABASE_URL`（test-rls
门禁，必须低权限角色）、`TEST_TENANT_DATABASE_URL`（test-pg-contracts，
须与前者不同）、`LLM_GATEWAY_TEST_PG_DSN`（视图契约 live）、
`ABS_SESSIONS_DIR`（取证/回放）、`TEST_DUAL_FULL_PGURL/_REDISURL`（dual
全链路）、`GATEWAY_URL`+`LLM_GATEWAY_API_KEY`（e2e）、`TEST_LIMIT`、
live 上游 key（`ZHIPU/MINIMAX/NVIDIA/GEMINI/OPENAI/XIAOMI/AA/VAPEUR_API_KEY`
等，未设自动 skip）。

其余测试基建变量（按需，均默认 skip 不报错）：`TEST_PG_URL`、`TEST_DB_URL`、
`DB_URL`、`DB_TEST_URL`、`TEST_REDIS_URL`、`TEST_SESSION_V2_ISOLATED`、
`TEST_PG_CONTRACTS_ISOLATED`、`LLM_GATEWAY_TEST_PG_URL`、
`LLM_GATEWAY_PG_TEST_URL`、`LLM_GATEWAY_PG_URL`、
`LLM_GATEWAY_FEATURE_STATS_TEST_DSN`、`BOOTSTRAP_TEST_DSN`、`GUARD_IT_DSN`、
`IDENTITY_ADMIN_DSN`、`IDENTITY_SHADOW_DSN`、`ROUTEINCIDENT_IT_DSN`、
`TOOL_REGISTRY_TEST_DB`、`OMNIFREE_TEST_DB_URL/_ADMIN_URL`、
`K2_MIGRATE_HELPER/_SHORT_APPLY_HELPER`、`K2_PREFLIGHT_HELPER/_SHORT_APPLY_HELPER`
（URSM↔K2 迁移预检）、`MIGRATE_TEST_KEY/_EMPTY/_UNSET`、`MOCK_FAST_PORT/_SLOW_PORT/_ERROR_PORT`、
`O5_FALLBACK_FILE_EVIDENCE`、`RJ_PGVAL_DOWN_SQL`、
`UPDATE_ANTHROPIC_STREAM_GOLDEN`、`LAUNCHER_TEST_MOCK_IMAGE`、
`LAUNCHER_E2E_GATEWAY_IMAGE`、`URSM_G4_REDIS_ADDR/_RESTART_CMD`、
`REPORT_E2E_ALLOW_DESTRUCTIVE`、`AUTO_AUDIT_TEST_MODE_TOKEN`、
`FREEDISCOVERY_DEFINITELY_UNSET_VAR_2`、`MARKER`、`KEEP_SLOG`、`PID_FILE`。

## 8. 系统与工具型变量

- 系统透传：`HOME`、`HOSTNAME`、`USER`、`http_proxy`/`https_proxy`/`HTTPS_PROXY`；
- 一次性命令行工具（`cmd/*`、`tools/*`）：`SERVER_PORT`（tests/local/gateway）、
  `REFRESH_INTERVAL_SECONDS`（licensing 健康检查）、`NO_INDEX`/`SPANS`
  （benchreport）、`DSN`（plugin-runtime sandbox 策略）、`MODEL_API_KEY`
  （sessionmeta-bench）；
- 调试开关：`CFG_DUMP_ALLOW_SECRETS`（配置导出含密钥，仅排障）、
  `REPORT_DUMP_SQL`（报表 SQL 打印）、`STICKY_MULTILEVEL_DEBUG`；
- 安装器族（`INSTALL_*`/`KX_*`/`INSTANCE_ID`/`HARDWARE_HASH`）见
  `10-installer.env.sample`；运维占位族（`HOST_*`/`SSH_*`/`KAIXUAN_*`）见
  `07-ops-servers.env.sample`。

## 9. 维护与对账

```bash
# 再生 settings 规格全表（specs 代码变更后必跑）
go run ./scripts/gen-env-ref > envs.samples/SPECS-REFERENCE.md

# 全仓库 env 审计 —— 稳妥启发式：任意"函数名(全大写字符串)"调用，
# 覆盖 os.Getenv/envOrDefault/getEnvBool/parseDurationEnv/applyPositiveIntEnv
# 等全部辅助读取函数；需人工剔除 Redis 命令字符串噪音（GET/POST/INCR/EXPIRE…）
git grep -hoE '\b[a-zA-Z][a-zA-Z0-9]*\("[A-Z][A-Z0-9_]+"' -- '*.go' ':(exclude)vendor/**' ':(exclude)*_test.go' \
  | sed -E 's/.*\("//;s/"//' | sort -u

# 结构体 env 标签与 settings 规格 EnvName
git grep -hoE 'env:"[A-Za-z0-9_]+"' -- '*.go' ':(exclude)vendor/**' | sed -E 's/env:"//;s/"//'
git grep -hoE 'EnvName:[[:space:]]*"[^"]+"' -- 'settings/*_specs.go' | sed -E 's/.*"//;s/"//'

# compose 插值与脚本
grep -rhoE '\$\{[A-Z0-9_]+' docker-compose*.yml scripts/ | tr -d '${' | sort -u

# 覆盖率对账：审计结果减去 envs.samples/*.sample 中的变量名，
# 剩余应只含 §7 测试基建与 §8 系统/工具型变量（及 Redis 命令噪音）
```

核心读取点：`config/config.go`、`config/storage.go`、`config/runtime_role.go`、
`cmd/gateway/main*.go`、`internal/logging/`、`domains/ursm/v2/config.go`、
`settings/specs.go`、`scripts/deploy-local-lib.sh`、`installer/`。
