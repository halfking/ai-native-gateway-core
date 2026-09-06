# REPO_LAYOUT — 仓库布局权威地图

> 本文是仓库顶层结构的**权威索引**（2026-08-17 结构治理后确立）。
> 新文件必须按下表入位；目录用途疑问先查本文，再查 [../README.md](../README.md)（docs/ 内部）与
> rule 36 归档协议（过程文档归 `docs/archive/`）。
> Go 包的**未来分层迁移**见 [../adr/ADR-0002-target-go-package-layout.md](../adr/ADR-0002-target-go-package-layout.md)。

## 阅读指引：一句话架构

主服务是单一二进制 `cmd/gateway`：mux → `domains/streaming`（ChatHandler）→
`domains/transformation`（OpenAI/Anthropic/Gemini IR 矩阵）→ `autoroute`/`domains/routing`
→ 凭证栈（`domains/credential`+`credentialfpslot`+`credentialhealth`+`pool`+`resolve`）→
`provider` → `upstream`，旁挂 `ratelimit`/`telemetry`/`metrics`。
`admin/`（Go 控制面，同进程同 mux）+ `web/`（Vue3 前端）构成管理台。

## 分类图例

- **[核心]** 请求服务链路，改动需回归测试
- **[控制面]** 管理台/后台子系统
- **[平台]** 跨域基础设施
- **[前端]** web/
- **[运维]** 部署、脚本、监控配置
- **[数据]** SQL/迁移
- **[文档]** docs/
- **[生成物]** gitignore 覆盖，不提交
- **[过程]** 会话过程状态（dot 目录），不参与归档

## 顶层目录

| 目录 | 分类 | 用途 |
|---|---|---|
| `cmd/` | 核心 | 22 个入口；**主服务 = `cmd/gateway`**，其余为工具（gateway-v2 为并行验证入口，sessionforensics、license-authority 等） |
| `domains/` | 核心 | 56 个限界上下文、约 18 万行；`streaming`（请求处理）、`transformation`（IR）、`routing`、`credential`、`session`(+v2)、`ursm/v2`、`hooks/*`、`pipeline`、`requestjourney` 等 |
| `domain/` | 核心 | 零依赖共享内核：`RequestEnvelope` + 9 个 context、TransportLayer 端口（见其 README/ARCHITECTURE） |
| `autoroute/` | 核心 | 路由引擎：pattern/LLM/embedding 分类器、打分、亲和、决策 v1/v2 |
| `provider/` | 核心 | 上游供应商客户端：计费、目录、候选探活、解密 |
| `upstream/` | 核心 | HTTP 上游客户端：代理解析、重试、错误分类 |
| `pool/` `resolve/` | 核心 | 每 identity 连接池 / 模型端点解析器 |
| `ratelimit/` `telemetry/` `metrics/` | 核心 | 限流 / 请求遥测生命周期 / Prometheus 指标 |
| `middleware/` | 核心 | HTTP 中间件（auth、cors、prometheus、requestid），用于 cmd+admin 面 |
| `internal/` | 平台 | 36 个子包：`ir`、`irconv`、`auth`、`outbox`、`streamretry`、`observability` 等。注意：当前**并非** Go 语义 internal（domains 有 97 处反向引用），见 ADR-0002 |
| `admin/` | 控制面 | 管理 HTTP API（约 350 文件），与数据面同进程同 mux |
| `api/` `apihub/` | 控制面 | 审批+钉钉 webhook / PG 资产仓 |
| `bg/` | 控制面 | 约 150 个后台 worker（探活、trimmer、聚合、刷新） |
| `center/` `licensing/` `settings/` | 控制面 | 实例中心 / License 与社区模式 / 运行时设置 |
| `plugin-runtime/` `maas/` `autoupdate/` | 控制面 | 插件运行时 / MaaS 计费 / 自更新 |
| `discovery/` `registry/` `metatools/` `catalog/` | 控制面 | 别名同步 / 工具注册 / 惰性工具类目 / 供应商标签 |
| `tenantops/` `vibecoding/` `i18n/` | 控制面 | 租户运维 / vibecoding 子系统 / 国际化 |
| `credentialfpslot/` `credentialhealth/` `security/` | 控制面 | 凭证指纹槽位 / 凭证健康 / armor+guardian+sanitize+sensitive |
| `config/` | 平台 | 配置加载 Go 包（内嵌 hooks.yaml 等） |
| `configs/` | 运维 | 环境脚本（env-252.sh 等）+ 敏感词数据；**无 Go** |
| `db/` | 平台 | DB 连接 Go 包 + `db/migrations/`（编号至 362，Go 加载） |
| `sql/` | 数据 | 规范 SQL 仓：`schema/`、`seed/`、`objects/`、`migrations/`（含 `startup/`、`timeout-optimization/`） |
| `deploy/` | 运维 | systemd/nginx/**k8s（含 cron）**/grafana/prometheus（alerts+rules+dashboards）/one-click + `deploy/sql/`（252 结构快照，待治理） |
| `scripts/` | 运维 | 分桶：`ops/` `test/` `partition/` `install/` `deploy/` `deprecated/` `rollback/` 等；一次性脚本入 `deprecated/` 或归档 |
| `installer/` | 运维 | **独立嵌套 Go module**（自有 go.mod），一键安装器 |
| `packaging/` | 运维 | 打包物料 |
| `web/` | 前端 | Vue3+Vite 管理台（内含 node_modules/dist，均已 ignore） |
| `tests/` | 测试 | 集成/回放/多模态测试 + `regression/`（路由回归）+ `local/`（本地 python 谐振测试） |
| `test/` | 测试 | Go 测试基建：`events/`（事件契约测试）、`mock/asm/` |
| `tools/` `examples/` `control/` `query/` | 平台 | specboost 工具 / 示例 / CQRS 写读路由句柄（B1） |
| `docs/` | 文档 | 见 [../README.md](../README.md)；过程文档走 rule 36 归档至 `docs/archive/` |
| `vendor/` | 平台 | go vendor（147 模块），`go mod vendor` 再生成 |
| `pkg/` | 平台 | 仅 `logger`（历史遗留，见 ADR-0002） |
| `adapter/` `cache/` `disguise/` `fault/` `hotconfig/` `envinjector/` `eventbus/` `errorsx/` `secret/` `durable/` `pending/` `modelcatalog/` `modelname/` `modeliqdata/` `observability→已并入 deploy` | 平台 | 单职责基础件（事件总线、错误、密钥、持久队列、待发件、模型目录/命名/IQ 数据、伪装、故障注入等） |
| `_to_be_deleted/` | 过程 | 退役代码快照，删除受 `MIGRATION-MANIFEST.md` 门禁（B1 预发通过+观察期） |
| `.agents/` `.kiro/` `.zcode/` `.scratch/` | 过程 | 会话/技能过程状态，docs-archive 明确排除，**不要归档**；`.handoff/` `.artifacts/` 已于 2026-09-07 清理并加入 gitignore（本地可再生成，不入库） |
| `bin/` `dist/` `build/` `logs/` `data/` `node_modules/` | 生成物 | 构建输出与运行态，gitignore 覆盖；`make clean` 清 bin/ 与根二进制 |

## 顶层文件

| 文件 | 说明 |
|---|---|
| `README.md` `CONTRIBUTING.md` `SECURITY.md` `LICENSE` `CHANGELOG.md` `CODE_OF_CONDUCT.md` `SUPPORT.md` `ROADMAP.md` | 项目门面；CHANGELOG 按 rule 36 每提交更新 |
| `docs/project-config.md` | agent 会话协议（session 先读；2026-09-07 自根目录迁入） |
| `Makefile` `go.mod` `go.sum` `VERSION` `version.json` | 构建/版本 SSOT = `version.json`（`build_seq` 已废弃删除） |
| `Dockerfile{,.incremental,.local-arm64,.web-patch}` `docker-compose{,.persistent,.dev-research,.deploy-test}.yml` | 镜像与编排 |
| `config.example.yaml` `.env.example` `.env.*.enc` `.sops.yaml` | 配置样例与 sops 密文 |
| `install.sh/.bat/.ps1` `uninstall.sh/.ps1` | **离线交付包入口**（`scripts/build-offline-packages.sh` 会复制到包根，勿移动）；三个安装入口各司其职：此处=客户离线包 / `deploy/one-click/`=整套环境部署 / `scripts/install/`=已装实例运维 |
| `LOCAL_CONFIG.md.template` | 本机配置模板 |

## 入位规则（新文件放哪）

1. **Go 业务代码** → 对应 `domains/<context>/`；跨域公共结构 → `domain/`；基础设施 → 现有平台包或 `internal/`（遵守 depguard）。
2. **一次性/排障脚本** → `scripts/ops/` 或 `scripts/deprecated/`（dated 事故脚本用后归档）。
3. **过程文档**（audit/fix/summary/report/handoff/phase/deploy-report）→ 直接写 `docs/archive/process/<category>/<YYYY-MM>/`，或结项后由 rule 36 流程归档；根目录与 `docs/` 根**禁止**散放。
4. **告警/监控配置** → `deploy/prometheus/rules/`。
5. **SQL 迁移** → `sql/migrations/`（启动种子 → `sql/migrations/startup/`；Go 内嵌迁移 → `db/migrations/`）。
6. **测试** → 就近 `_test.go`；跨包集成 → `tests/`；本机手工测试 → `tests/local/`。
7. **构建产物** 永不提交：二进制输出用 `bin/`，发布包出仓库存放（`dist/` 仅本地暂存）。

## 已知后续项（本轮未做）

- `deploy/sql/schemas/baseline/` 与 `sql/schema/` 为有意维护的双副本（installer 嵌入用，
  `migrate-sql-files.sh` 同步）——保持现状，改动 schema 时记得双侧同步。
- Go 包分层迁移 → ADR-0002。
- git 历史中的大二进制（约 70MB）与已泄漏密钥 → 需历史重写+密钥轮换专项。
