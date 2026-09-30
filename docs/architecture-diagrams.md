# 系统架构与业务流程图集（Mermaid）

> **事实快照**: 2026-10-01 · 基线提交 `3efae99`（main）
> **绘图方式**: 全部图表为 Mermaid 代码块，可在支持 Mermaid 的 Markdown 渲染器（GitHub / VS Code / GitLab）直接查看。
> **证据优先级**: 运行时 wiring / Go 代码 / SQL migration > [03-design/01-architecture/architecture/ARCHITECTURE.md](03-design/01-architecture/architecture/ARCHITECTURE.md)（2026-10-01 全码审计快照）> 本文档 > 历史审计。
> **配套文档**: 会话全生命周期专图见 [session-lifecycle.md](session-lifecycle.md)；异常分支细图见 [REQUEST_FLOW_WITH_EXCEPTIONS.md](REQUEST_FLOW_WITH_EXCEPTIONS.md)。

---

## 1. 系统上下文图（谁在和网关打交道）

```mermaid
graph TB
    subgraph clients["客户端层"]
        AGENT["AI Agent / IDE"]
        APP["业务应用 / API 调用方"]
    end
    ADMIN["管理员浏览器<br/>Vue 3 SPA"]
    MAINTAIN["ai-native-maintain<br/>License / 升级 / 分发控制面"]
    ASM["ai-session-manager (ASM)<br/>会话投影 / 分析 / 审计治理"]
    MASTER["主控端 llmgateway.internal.example.com<br/>在线激活 / 实例注册"]

    AGENT -->|"OpenAI / Anthropic /<br/>Gemini / Responses HTTP+SSE"| GW
    APP -->|"Bearer API Key"| GW
    ADMIN -->|"/admin + /api/admin/*"| GW
    MAINTAIN <-->|"/maintain-api/* 兼容反代"| GW

    GW["llm-gateway-go<br/>cmd/gateway 单进程巨石<br/>数据面 / 控制面 / 管理面同 mux"]

    GW -->|"Provider API<br/>OpenAI / Anthropic / Gemini /<br/>MiniMax / Zhipu / DeepSeek / Doubao / Ernie …"| PROVIDERS["上游 LLM Provider"]
    GW -->|"signed/API 事件契约<br/>(ASM outbox)"| ASM
    GW <-->|"激活 / 心跳 / 升级包"| MASTER

    subgraph storage["持久化与观测"]
        PG[("PostgreSQL 15+<br/>RLS 38+ 表 / 月分区")]
        REDIS[("Redis 7+<br/>URSM 状态 / 限流 / 会话热状态")]
        OSS[("OSS / S3 / 本地文件<br/>请求体归档")]
        PROM["Prometheus /metrics<br/>200+ 指标"]
        OTEL["OpenTelemetry Collector<br/>(可选)"]
    end
    GW -.-> PG
    GW -.-> REDIS
    GW -.-> OSS
    GW -.-> PROM
    GW -.-> OTEL
```

要点（证据：`cmd/gateway/main.go` 装配，ARCHITECTURE.md §2）：

- 网关是**单进程巨石**：数据面（用户 LLM 请求）、控制面（Admin API + Vue SPA）、管理面（licensing/fault/autoupdate 等 Echo 子应用）共用一个 HTTP mux，h2c 同端口承载 HTTP/1.1 与 HTTP/2（main.go:7201）。
- 三服务边界：Gateway 是 provider 调用与 canonical 事实的 Owner；ASM 只消费签名事件做投影分析；Maintain 负责 License/升级（Gateway 保留 `/maintain-api/*` 兼容反代）。

---

## 2. 容器级架构图（网关内部结构）

```mermaid
graph TB
    subgraph gateway["cmd/gateway（单进程）"]
        direction TB
        MW["HTTP 中间件链<br/>Recovery→RequestID→Locale→CORS→Prometheus→<br/>Tracing→Auth→Origin→Logging→SecurityHeaders<br/>(main.go:7111-7124)"]

        subgraph dataplane["数据面 Data Plane"]
            HANDLER["协议 Handler<br/>Chat / Messages / Responses /<br/>Embeddings / Gemini / HostedTasks<br/>(domains/streaming/handler.go:1917)"]
            EXECUTOR["Executor<br/>attempt 预算 / continue 检测 /<br/>身份池 Layer0<br/>(domains/streaming/executors/executor.go:2114)"]
            DISPATCH["Dispatch Pipeline（唯一执行路径）<br/>T1 总队列→T3 model 队列→<br/>T5 credential 队列→forwarder<br/>(domains/dispatch)"]
            ROUTER["Router.PlanCandidates<br/>tier / 健康 / sticky / 压力过滤<br/>+ URSM v2 评分 + P2C<br/>(executors/router.go:220)"]
            GATE["资源门<br/>FP 槽位 / 并发信号量 / RPM"]
            UPSTREAM["Upstream Client + identity-bound 连接池<br/>IR/协议转换 + vendorstrip + SSE 回写"]
        end

        subgraph controlplane["控制面 Control Plane"]
            ADMINAPI["Admin API<br/>/api/auth/* + /api/admin/*<br/>(admin/ 531 文件)"]
            ECHOOPS["Echo 运维子应用<br/>licensing / fault / autoupdate /<br/>center / vibecoding / tenantops"]
            WEBUI["Vue 3 SPA<br/>(web/ dist 从磁盘服务)"]
        end

        BG["后台 Worker（bg/ 约 80+ goroutine）<br/>探测 / 清理 / 统计聚合 / 分区维护 /<br/>质量采集 / 对账 / 物化视图"]
        HOTCFG["热配置 (~5s 生效)<br/>settings + hotconfig"]
    end

    MW --> HANDLER
    HANDLER --> EXECUTOR
    EXECUTOR --> DISPATCH
    DISPATCH --> ROUTER
    ROUTER --> GATE
    GATE --> UPSTREAM

    ADMINAPI --> PGW[(PostgreSQL)]
    ADMINAPI --> RW[(Redis)]
    dataplane --> PGW
    dataplane --> RW
    BG --> PGW
    BG --> RW
    UPSTREAM -->|"HTTP/SSE"| P["Provider APIs"]
    WEBUI -.->|"同源静态资源"| ADMINAPI
```

**数据面公共 API 面**（AUTHITECTURE.md §3 + `domains/streaming` 路由注册）：

| 端点 | 协议 |
|---|---|
| `/v1/chat/completions` · `/v1/completions` | OpenAI 兼容 |
| `/v1/messages` · `/v1/messages/count_tokens` | Anthropic 兼容 |
| `/v1/responses` | Responses API |
| `/v1/embeddings` · `/v1/models` | OpenAI 兼容 |
| `/v1beta/models/{model}:generateContent` 等 | Gemini native |
| `/v1/hosted-tasks` | 托管任务门面（create/status/result/cancel/recall） |

---

## 3. 数据面请求处理主链（业务流程图）

```mermaid
graph TB
    START(["客户端请求到达<br/>HTTP/1.1 或 h2c HTTP/2"]) --> MW["中间件链<br/>Recovery→RequestID→Locale→CORS→<br/>metrics→Auth→Origin→Security"]
    MW -->|"401/403"| E4XX(["鉴权失败返回"])
    MW --> AUTH["API Key 验证<br/>→ tenant / user / key 三级身份"]
    AUTH --> PARSE["协议解析 + IR 归一<br/>internal/ir + domains/transformation"]
    PARSE -->|"400"| EPARSE(["解析/参数失败"])
    PARSE --> SESSION["会话指派 assignGatewaySession<br/>（详见会话生命周期文档 §2）"]
    SESSION --> SEC["安全钩子<br/>promptinjection / outputcompliance / secretmask"]
    SEC --> AUTO{"model=auto?"}
    AUTO -->|"是"| L1["L1 自动选模型<br/>任务分类 + 6 维评分<br/>(autoroute/, AUTO_ENABLE_V2 默认 false)"]
    AUTO -->|"否，显式 model"| L2IN["模型解析 / models_canonical"]
    L1 --> RES
    L2IN --> RES["资源准备<br/>语义缓存检查 / 提示词压缩(上下文 ~80% 触发) / 附件"]
    RES --> EXEC["Executor.Execute<br/>attempt 预算 / continue 检测"]
    EXEC --> DISP["Dispatch 队列<br/>T1 总队列 → T3 model 队列 → T5 credential 队列"]
    DISP --> PLAN["Router.PlanCandidates<br/>候选过滤: availability/tenant/protocol/tier/billing/sticky"]
    PLAN -->|"无候选"| E503(["503 no-route"])
    PLAN --> P2C["URSM v2 评分 + P2C 排序<br/>成本惩罚 0.15 默认折入<br/>(LLM_GATEWAY_ROUTING_W_COST 可关)"]
    P2C --> RGATE["资源门: FP 槽位 / 并发 / RPM<br/>(TPM 完整接线为 TARGET)"]
    RGATE -->|"429/503"| FAILOVER["切下一候选 tiered failover"]
    FAILOVER --> DISP
    RGATE --> UP["上游调用 upstream.Client<br/>0 内部重试 + IR/协议转换 + vendorstrip"]
    UP --> FBB{"首字节边界"}
    FBB -->|"首字节前失败"| PRERETRY["可安全重试 / failover<br/>(streamretry 可选, 默认 OFF)"]
    PRERETRY --> DISP
    FBB -->|"流建立"| SSE["SSE 中继回写客户端<br/>行限 SSEMaxLineBytes / keepalive /<br/>完整性检测 vendorstrip"]
    SSE --> DONE(["流结束 / 非流式响应返回"])
    SSE -.->|"首字节后错误"| INLINE["错误内联进流<br/>不破坏已输出语义"]
    INLINE --> DONE
    DONE --> PERSIST["持久化链<br/>request WAL → request_logs_hot + usage ledger<br/>→ onPersisted 钩子"]
    PERSIST --> DERIVE["派生链(异步)<br/>Session V2 影子写 / session_dim UPSERT /<br/>ASM outbox / 统计 / Prometheus / OTel"]
    DERIVE --> STICKY["sticky 绑定写回<br/>L1/L2/L3 三级"]
```

重试与流一致性的核心规则（runtime-request-flow.md §4）：

- **首字节前**：可按策略重试 / failover（换 credential / provider / model）；多层重试能力并存（dispatch timed retry、goal retry、request survival、可选 stream retry），统一 retry budget 仍是 TARGET。
- **首字节后**：遵循流一致性，不把已输出流重放到另一个 Provider；错误内联进 SSE。
- 关联键不变量：retry/failover 只增加 `attempt_id`，不复制 `turn_id`；`charge_id` 承担计费幂等。

---

## 4. 智能路由决策图（双层路由）

```mermaid
graph TB
    subgraph L1["L1 模型选择（model=auto 时）"]
        CLS["Prompt 任务分类<br/>classifier.go 8 类 / classifier_v3.go 10 类<br/>(flag auto_v3_enhanced_classification)"]
        SCORE6["6 维评分<br/>质量 / 速度 / 成本 / 上下文 / 多模态 / 可用性"]
        LOCK["Profile 锁定<br/>显式 model 参数绕过 L1"]
        CLS --> SCORE6 --> PICK["选定目标模型"]
        LOCK -.->|"bypass"| PICK
    end

    subgraph L2["L2 凭据选择（每个请求）"]
        FILTER["候选过滤<br/>availability / tenant / protocol / model<br/>lifecycle / routable / probe / health 门"]
        URSMS["URSM v2 状态<br/>off / shadow / canary / authoritative 四档<br/>(main.go:1067-1205)"]
        TIER["tier 回退 1→2→3→9<br/>billing 轮次: metered→free→quota"]
        STICKYIN["sticky 约束<br/>会话→凭据绑定, 跨模型保持"]
        P2CS["P2C 评分<br/>并发/身份/延迟/质量/余量/容量惩罚<br/>+ 成本惩罚 0.15 + sticky 负载 0.15<br/>+ recency 0.05 / PAYG 余额 0.05 / 计划额度 0.10"]
        FILTER --> URSMS --> TIER --> STICKYIN --> P2CS
    end

    REQ["请求"] --> L1
    REQ --> L2
    P2CS --> ACQ["资源 acquire<br/>FP slot / concurrency / RPM"]
    ACQ --> FWD["forward 尝试"]
    FWD --> OUTCOME["结果回流<br/>健康分 / 用量 / 路由观测 / sticky 写回"]
    OUTCOME -.->|"失败降级<br/>rate-limited/cooling/unreachable→suspended"| URSMS
    OUTCOME -.->|"冷却后自动恢复"| URSMS

    PROBE["后台探测<br/>credential_selfcheck / node_probe /<br/>system_health (新族默认接管)<br/>错误门控: 成功停放 30d / 两连成功早停"] -.-> URSMS
```

状态源与评分细节证据：`domains/streaming/executors/router.go`、`router_scoring.go`、`domains/ursm/v2/`、`internal/probemode`（详见 [routing-and-state.md](03-design/01-architecture/architecture/routing-and-state.md)）。

---

## 5. 核心领域模块图（DDD 分层）

```mermaid
graph TB
    subgraph entry["入口 / 组装层"]
        MAIN["cmd/gateway/main.go<br/>composition root (~7800 行)<br/>+ ~90 个 main_* 接线文件"]
    end

    subgraph dp["数据面领域 (domains/)"]
        STREAMING["domains/streaming<br/>请求处理 / SSE 中继 / 候选执行"]
        DISPATCH["domains/dispatch<br/>队列 / forwarder / governor / failover"]
        CREDENTIAL["domains/credential<br/>FP 槽位 / 并发 / 健康指纹"]
        URSM["domains/ursm (v2)<br/>路由实时状态 (Redis/Lua)"]
        SESSION["domains/session (+v2)<br/>会话热状态 / V2 影子写"]
        IR["internal/ir + domains/transformation<br/>协议归一 (OpenAI/Anthropic/<br/>Gemini/Responses/Ollama)"]
        VSTRIP["internal/vendorstrip<br/>厂商私有字段过滤"]
        HOOKS["domains/hooks<br/>audit / security / observability"]
    end

    subgraph cp["控制面领域"]
        ADMIN["admin/ (531 文件)<br/>REST API"]
        CONTROL["control/<br/>CQRS 半迁移 (routing create)"]
    end

    subgraph infra["基础设施层"]
        STORE["storage/<br/>full (PG+Redis) / lite (SQLite+文件) 工厂"]
        TELEMETRY["telemetry / metrics / exporter"]
        ERRORSX["errorsx<br/>7 类错误分级 / failover 策略"]
        RATELIMIT["ratelimit/<br/>RPM / TPM Redis Lua 滑窗"]
        EVENTBUS["eventbus / durable outbox"]
    end

    subgraph bglayer["后台层"]
        BGW["bg/ (294 文件, ~80+ worker)<br/>探测 / 生命周期清理 / 统计聚合 /<br/>分区维护 / 质量采集 / 对账"]
    end

    MAIN --> STREAMING
    MAIN --> ADMIN
    MAIN --> BGW
    STREAMING --> DISPATCH --> CREDENTIAL
    STREAMING --> SESSION
    DISPATCH --> URSM
    STREAMING --> IR --> VSTRIP
    STREAMING --> HOOKS
    ADMIN --> CONTROL
    STREAMING --> STORE
    DISPATCH --> ERRORSX
    CREDENTIAL --> RATELIMIT
    HOOKS --> TELEMETRY
    SESSION --> EVENTBUS
```

> 并行实现警示（ARCHITECTURE.md §8）：autoroute 三代（decision v1/v2 双活）、Session V1/V2、探测新旧 worker 族、URSM key 三代 schema、outbox 4 套实现等 **10 组并行对 + 7 处死代码** 已登记，读代码时勿把演示入口/死代码当活跃路径。

---

## 6. 存储架构图（full / lite 双模式）

```mermaid
graph TB
    subgraph full["Full 模式（默认, LLM_GATEWAY_STORAGE_MODE 未设或 full）"]
        PGX[("PostgreSQL 15+")]
        subgraph pgtables["核心事实与配置"]
            TENANTS["tenants / users / api_keys<br/>(RLS 行级安全 38+ 表)"]
            CREDS["credentials / providers / models"]
            REQLOGS["request_logs (月分区父表)<br/>request_logs_hot / usage ledger<br/>764: tenant+ts 分区索引"]
            SESSPG["sessions 族 (V2, shadow)<br/>sessions / session_turns /<br/>session_bodies / session_turn_logs(24h TTL)"]
            DIMS["session_dim / session_summaries /<br/>routing_attempts / auto_route_selections"]
        end
        PGX --- pgtables
        REDISX[("Redis 7+")]
        subgraph rediskeys["热状态"]
            URSMR["URSM 状态 (Lua)"]
            STICKYR["sticky 三级绑定<br/>L1 1h / L2 24h / L3 7d"]
            SESSR["session:{id} 会话热状态 Hash<br/>(TTL 过期)"]
            LIMITR["限流桶 RPM/TPM + FP 槽位"]
            QUEUES["实时请求流队列 / 锁"]
        end
        REDISX --- rediskeys
    end

    subgraph lite["Lite 模式 (LLM_GATEWAY_STORAGE_MODE=lite, 零依赖单二进制)"]
        SQLITE[("SQLite (WAL)<br/>会话/请求元数据/目录")]
        FILES[("本地文件<br/>请求体异步写 + L1.5 缓存")]
        MEMKV["进程内 KV<br/>lite.MemoryStateStore"]
    end

    FACT["storage/factory 统一接口分发"] -->|"full"| PGX
    FACT -->|"full"| REDISX
    FACT -->|"lite"| SQLITE
    FACT -->|"lite"| FILES
    FACT -->|"lite"| MEMKV
    GWPROC["cmd/gateway"] --> FACT

    ARCH["请求体归档<br/>requestdetail + secretmask 脱敏"] --> OSS[("OSS / S3 / 磁盘")]
    REQLOGS -.->|"分区保留期外<br/>754 archive_request_logs_default"| ARCHT["request_logs_archive_YYYY_MM<br/>(仅摘要字段, ~5% 体积)"]
```

---

## 7. 部署架构图（M1–M4 与三服务拓扑）

```mermaid
graph TB
    subgraph modes["四种部署模式"]
        M1["M1 二进制 + systemd<br/>外部 PG/Redis<br/>(installer 支持, CURRENT)"]
        M2["M2 Docker Compose<br/>quickstart: PG+Redis+网关<br/>(CURRENT)"]
        M3["M3 K8s 部署/.sidecar<br/>(测试级 manifests, PARTIAL)"]
        M4["M4 K8s Operator CRD<br/>(TARGET 未实现)"]
    end

    subgraph prod["生产拓扑（逻辑视图）"]
        LB["反向代理 / TLS 终结"]
        GWN["网关节点 ×N<br/>(无状态, 状态在 PG/Redis)"]
        PGPOOL[("PostgreSQL<br/>主从/备份")]
        RD[("Redis<br/>多实例共享状态")]
        MAINTAINN["ai-native-maintain<br/>License/升级控制面"]
        ASMN["ai-session-manager<br/>会话投影/分析"]
        UPSTREAMS["Provider APIs"]
        LB --> GWN --> UPSTREAMS
        GWN --> PGPOOL
        GWN --> RD
        GWN <-->|"/maintain-api/* 反代"| MAINTAINN
        GWN -->|"outbox 事件"| ASMN
    end

    INSTALLER["installer/ (独立 Go module)<br/>安装 / 升级 / 回滚 / launcher<br/>升级前自动备份 + 失败回退"]
    INSTALLER -.-> M1
```

升级与激活（PROJECT_OVERVIEW §3.1/§3.10）：在线激活（JWT 24h 续期）/ 离线激活 / 7 天试用；自动升级 6h 检查周期，升级后 5s 健康检查失败自动回退。

---

## 8. 后台 Worker 体系图

```mermaid
graph TB
    MAINBG["cmd/gateway main.go:3626-5780<br/>bg worker 装配 (graceful shutdown 23s HTTP→5s worker)"]

    subgraph probes["探测与恢复"]
        NEWPROBE["新 worker 族 (默认接管, internal/probemode 唯一权威)<br/>credential_selfcheck / node_probe / system_health"]
        OLDPROBE["legacy 5 worker<br/>(双门 flag 才启动, 对象仍装配注入 admin)"]
        HEALTH["健康追踪: 滑窗 / 自动恢复 /<br/>并发自扩容 / 峰值"]
    end

    subgraph lifecycle["数据生命周期"]
        PARTMGR["partition_manager<br/>request_logs 归档(754) / bodies 分区 DROP /<br/>session_turn_logs TTL(24h) / VACUUM"]
        TRIMMERS["audit_trimmer / opslog_trimmer /<br/>session_summaries_trimmer(90d) / 热区维护"]
    end

    subgraph analytics["分析与统计"]
        STATS["统计聚合: 分钟/日/月汇总 + 对账"]
        AUTOROUTE["autoroute 结算 / 亲和 / 特征刷新"]
        QUALITY["质量采集 / 模型画像 / modelquality"]
        SESSH["session_health_worker (1h)<br/>会话健康分计算"]
        MV["物化视图刷新"]
    end

    subgraph platform["平台同步"]
        APIHUB["apihub 同步"]
        GOALRUN["goalrun 调度"]
        SYSMON["systemmonitor: Redis 队列 + 30s 去重"]
    end

    MAINBG --> probes
    MAINBG --> lifecycle
    MAINBG --> analytics
    MAINBG --> platform
    NEWPROBE -.->|"健康结果"| URSMSTATE["URSM/候选可用性"]
    PARTMGR --> PGW[("PostgreSQL")]
    STATS --> PGW
```

> 注意：`bg/session_lifecycle_worker.go`（会话闲置回收）经 R35 审计标注 **UNUSED / 零生产调用方**，不得当作活跃路径；会话数据的实际终结链路见 [session-lifecycle.md](session-lifecycle.md) §8。

---

## 9. 可观测性链路

```mermaid
graph LR
    REQ["请求"] --> MW["中间件/Handler 埋点"]
    MW --> PROM["Prometheus /metrics<br/>请求量/延迟/错误/凭据健康/<br/>路由决策/连接池"]
    MW --> OTEL["OTel Trace (可选)<br/>tenant/session/request id 传播"]
    MW --> SLOGX["结构化日志 slog (JSON)"]
    MW --> RLOG["request_logs + WAL<br/>(canonical 事实)"]
    RLOG --> JOURNEY["requestjourney 观测<br/>(admin 决策回放)"]
    RLOG --> LIVESTREAM["Admin 实时请求流<br/>Redis FIFO + 去重"]
    PROM --> GRAF["Grafana"]
    SLOGX --> BODYARCH["请求/响应体归档<br/>(secretmask 脱敏后)"]
```

---

## 10. 图集与既有文档的关系

| 图 | 补充的既有文档 |
|---|---|
| §1–§2 架构图 | [PROJECT_OVERVIEW.md](PROJECT_OVERVIEW.md)、[architecture.md](architecture.md)（英文版） |
| §3 请求主链 | [REQUEST_FLOW_WITH_EXCEPTIONS.md](REQUEST_FLOW_WITH_EXCEPTIONS.md)（异常分支细图）、[runtime-request-flow.md](03-design/01-architecture/architecture/runtime-request-flow.md) |
| §4 路由决策 | [routing-and-state.md](03-design/01-architecture/architecture/routing-and-state.md)、[AUTO_SELECTION_SPEC](03-design/02-feature-design/design/AUTO_SELECTION_SPEC.md) |
| §5 领域模块 | [MODULES_GUIDE.md](MODULES_GUIDE.md)、[parallel-implementations-comparison.md](03-design/01-architecture/parallel-implementations-comparison.md) |
| §6 存储 | [storage/README.md](storage/README.md)、[lite-mode-quick-ref](03-design/design/lite-mode-quick-ref.md) |
| §7 部署 | [06-deployment/README.md](06-deployment/README.md) |
| 会话生命周期 | **[session-lifecycle.md](session-lifecycle.md)**（本图集的姊妹篇） |

---

## 附录：本文档引用的关键代码锚点

| 锚点 | 位置 |
|---|---|
| HTTP mux / h2c | `cmd/gateway/main.go:7201` |
| 中间件链 | `cmd/gateway/main.go:7111-7124` |
| ChatHandler 入口 | `domains/streaming/handler.go:1917` |
| serveWithExecutor | `domains/streaming/handler.go:2249` |
| Executor.Execute | `domains/streaming/executors/executor.go:2114` |
| Router.PlanCandidates | `domains/streaming/executors/router.go:220` |
| URSM 四档装配 | `cmd/gateway/main.go:1067-1205` |
| Admin Echo 子应用 | `cmd/gateway/main.go:6520-6698` |
| bg worker 装配 | `cmd/gateway/main.go:3626-5780` |
| 存储模式装配 | `cmd/gateway/storage_mode_init.go` |
| request_logs 归档函数 | `sql/migrations/startup/754_archive_request_logs_default.sql` |

---

**文档版本**: v1.0（2026-10-01）
**维护**: 与 [ARCHITECTURE.md](03-design/01-architecture/architecture/ARCHITECTURE.md) 同步更新；图表事实漂移时以代码 wiring 为准并回改本文。
