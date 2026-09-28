# R76 · R73 批判式审计续审记录

> 日期：2026-09-28（Asia/Shanghai）
> 状态：部分证据完成；D01–D17 全域审计、真实部署环境验证和完整迁移设计仍开放。
> 起始基线：本机 main 与 origin/main 同为 02a6f9dd39b57a630b16580170833f822d6a3632。续审期间并行提交 b75b032c5193c057d0a8baae17614e6af3265c56 已同时落在 HEAD/origin/main；未覆盖该提交。未执行 fetch、pull、部署或远端数据库操作。

## 本轮核对边界

- 工作区初始仅有未归属的 web/public/menu-config.json 修改及 docs/audit/todo-state.json 未跟踪文件。两者未编辑、暂存或提交。
- PostgreSQL 验证运行于无 host port 映射的临时 Docker PostgreSQL 17 容器；没有连接共享或生产数据库。
- 本轮未连接真实 Redis、provider，也未运行本地部署流程。全仓 race 既有失败未在隔离环境复核。

## 并行提交 b75b032 的窄范围复核

续审期间 commit b75b032c5193c057d0a8baae17614e6af3265c56 同步到本地和 origin。该提交将 MaterializedViewRefresher.SetDistLock 与 SetAlertCallback 的契约更正为必须在 Start() 前调用，并在 dispatch 选择器 flag-off 的 Debug 日志前增加 Enabled 检查。

- 全仓生产调用点仅在 cmd/gateway/main.go 设置这两个字段；两处都在同一 refresher 的 Start() 前。setter 本身仍不具备并发安全，后续调用方必须遵守启动前配置契约。
- go test -race ./bg -run 'TestMaterializedViewRefresher' -count=1：通过。
- go test -race ./domains/streaming/executors -run 'TestSelectDispatchEndpointFlagsAndFallback|TestDispatchRouteSoftRankRaceUnderConcurrentDispatch' -count=1：通过。
- 本轮没有重跑全仓 race；此前记录的 dispatch/network race 失败仍需隔离复核。

## R73-F07 · migration 754

### 生产调用链与可触发边界

bg/partition_manager.go 的 archiveOldRequestLogs 在本地 03 点小时窗口调用 archive_request_logs_default，并给单次函数调用设置 30 分钟 Go context。函数枚举 public 下超过 retention 的 request_logs_YYYY_MM 分区，逐月获取事务级 advisory lock，每批 1000 行投影写入 request_logs_archive_YYYY_MM，依赖 (request_id, ts) 唯一索引做 ON CONFLICT DO NOTHING。函数没有 SET LOCAL statement_timeout；当前调用方在显式事务内先执行 SET LOCAL statement_timeout = 30min，再调用集合函数；Go context 同样为 30 分钟，事务结束后 SET LOCAL 自动还原 role/session 原值。Go context 单独不会覆盖 PostgreSQL GUC；本地调用方的事务抬升解决了 role 默认 30 秒在 30 分钟预算之前杀死语句的问题。若调用仍超过 30 分钟，整次语句/事务回滚，下一日仍会重扫；没有 archive ledger 可跳过完成分区。函数不删除源行。

运行角色还需有源分区读取权限、public schema 的 CREATE 权限及归档写入权限。隔离 PG 17 上，无 CREATE ON SCHEMA public 的普通 role 在首次建归档表时收到 permission denied for schema public；授予 CREATE 后调用成功。仓库 scripts/grant-permissions.sh 含该授权，但这轮没有证明所有既有安装/升级路径都执行过该脚本；生产 role 权限仍需独立确认。

### 非生产数据库实测

在一次性 PostgreSQL 17 容器中建立两个旧月分区，插入 120,000 + 30,000 行合成数据后执行 canonical migration：

- 首次调用返回 150,000 条新增归档记录，两个归档表分别为 120,000 与 30,000 行。
- 源 request_logs 仍有 150,000 行；迁移未删除或截断源数据。
- 幂等重跑返回 0 条新增记录，归档行数和源行数保持不变。
- 普通角色设置 role default statement_timeout=1ms 后，直接调用函数可复现 PostgreSQL 的 statement timeout 取消；随后按当前生产顺序执行 BEGIN → SET LOCAL statement_timeout='30min' → archive function → COMMIT，成功归档 150,000 行。COMMIT 后 SHOW statement_timeout 仍为 1ms，证明连接 session 的 role 默认值已恢复。
- 该 150,000 行合成规模不足以代表生产容量或最大执行时长。未运行 Citus 镜像、生产数据 EXPLAIN/ANALYZE，也未测真实 role 的 timeout。
- go test ./bg -run 'TestArchiveOldRequestLogs_StatementTimeoutPinnedInsideTx|TestArchiveOldRequestLogs_SumsEveryPartition|TestArchiveOldRequestLogs_NilPoolIsNoOp' -count=1：退出码 0。该 Go 测试验证调用代码的事务/SET LOCAL/查询/提交顺序；PG 实测使用 psql 在隔离容器复现相同顺序。

结论：补齐了 vanilla PostgreSQL 17 的语法、首次归档、幂等重跑、源数据保留、角色权限、默认 statement_timeout 取消、调用方 SET LOCAL 抬升及提交后重置的证据；F07 仍为部分验证，不得关闭。后续在安全隔离的 Citus/PG 版本上用代表性数据量测量单次 30 分钟预算内的总耗时、锁等待和磁盘/WAL 增量；同时核实实际 app role grant。没有达到预算的结果须设计可续跑 ledger/独立提交方案并使用新的前向迁移，不修改已发布 migration 754。

## R73-F09 · ShouldBlock 客户端终态

security/sanitize.SanitizeRestoreInterceptor.InterceptStreamChunk 在 lane 上限耗尽或无法安全消费 opaque continuation 时返回 ShouldBlock=true 并将该 writer 的 sanitize state 标成 blocked。已暂存的疑似 marker 尾片在流结束时也没有终判：InterceptStreamEnd 只消费重组后的 capture，并不将 streamRestoreState.tails 写回 client stream。因此如果普通文本以一个被暂存、但最终不构成敏感 marker 的前缀结束，这段文字会静默丢失，属于 P3 保真度缺陷；如 marker 确实不完整，丢弃则是当前 fail-closed 选择。domains/streaming/interceptingStreamWriter.writeFrame 收到该结果后只 return，不设置 writer error、不发 SSE 终态错误、不记阻断指标；后续帧也被拦截器持续标记并丢弃。触发时客户端会收到此前已刷出的部分内容，之后流静默缺帧并等到 upstream 结束/连接关闭；当前缺少 writer 到客户端可识别失败的集成断言。

该行为符合“不泄露无法关联的 marker continuation”的 fail-closed 数据选择，但传输闭环和可观察性不足，登记为开放 P2。修复前需选定跨 OpenAI Chat、Anthropic、Responses 的共同终止策略：将拦截阻断转为 writer/upstream 取消错误，由已有协议 coordinator 产生合法 failure terminal，或者在 headers 已发送后主动关闭流并记录稳定错误原因；禁止将其伪装成成功结束。增加低基数计数器/结构化事件，仅记录 request/session ID、协议、lane-limit/opaque-continuation reason，不记录 payload 或 marker 值。回归需证明敏感尾片及后续帧不外泄、客户端能识别非成功终态、durable stream 状态和错误遥测一致。

## R62 凭据风险：兼容迁移决策草案

本轮逐段复核当前源码路径，四项均仍可定位；未修改实现或数据库 schema。

| ID | 当前触发链 | 兼容处理方案 | 前向迁移与回滚要求 |
|---|---|---|---|
| H1 activation code | offline approval 写入 offline_activation_requests.activation_code；审批重试/通知读取；ops overview 返回明文；日志 notifier 与审批日志也输出 code | 采用版本化 AEAD envelope 加密存储，而非仅 hash：当前重审批和通知都需要再次取回原 code。新增 ciphertext/key-version 字段；运维列表只返回掩码。确需交付 code 的授权动作继续解密，并记录操作者与 request ID。日志只留 request ID 和不可逆指纹。 | 新版本先双写 encrypted + legacy plaintext、优先读 ciphertext 并兼容旧行；批量加密回填、逐批验证解密及记录数；确认通知重试/重复审批/历史未消费请求行为后停止 plaintext 写入；经过回滚窗口再由后续前向迁移清空/移除 legacy 列。密钥来自独立 secret/KMS，保存 key version、备份/轮换/恢复演练；缺 key 时拒绝明文 fallback。 |
| H2 refresh token | refresh handler 收到 token → GetInstanceByRefreshToken 明文等值查询 → 签发新 token → UpdateRefreshToken 明文轮换 | 用独立 pepper 的 HMAC-SHA256 查找指纹，按 pepper key version 保存 hash；token 原文只在签发响应与客户端专用 0600 文件中出现。 | expand 增 hash/version 列和索引；双读 legacy 与 hash、双写过渡并将旧 token 分批 HMAC 化；按当前及前一 pepper 版本验证，轮换后渐进重算；确认 90 天 token 生命周期和未活跃实例策略后清空旧明文。回滚版本需可读 hash 新数据，或保持旧列直到整个回滚窗口结束；不得把已撤销 token 重新激活。 |
| M1 trial license key | trial API 返回 key → installer 把 key 写入 ActivationState.LicenseKey → activation.json（0600）在注册成功与失败状态均落盘 | activation.json 只留状态、到期时间及非凭据指纹；为“trial 成功但 register 暂时失败”的恢复语义定义独立 trial-license.key（0600），并让重试代码明确读取它；register 成功后移除。launcher /status 保持 allowlist，不传 key。 | 先增加重试读取并测试旧 activation.json 向后读；新写入不再序列化 license_key；只在待恢复时原子写独立 key 文件，成功时清除。旧状态文件重写时去除字段。确认备份/诊断包排除该文件；不得假设 unlink 可物理擦除 SSD。 |
| M2 trial email uniqueness | trial 请求将 email trim/lower 后只靠 Redis SETNX 365 天占用；DB transaction 插入 trial license 与 consent，但 DB 未强制 email 唯一 | 以数据库为最终原子门：规范化 email，并对 trial licenses 建 partial unique index（例如 lower(btrim(customer_email)) WHERE subscription_tier='trial'）；保留 Redis rate limiter，不把 Redis 作为唯一性来源；将 unique violation 映射成 409。需 owner 决定“永不过期唯一”是否是产品契约，因 Redis key 当前 365 天过期。 | 先只读盘点重复 email，人工裁定冲突，不静默删许可证；并发请求测试证明数据库唯一约束只发一张 trial。生产表大时评估 CREATE UNIQUE INDEX CONCURRENTLY（不可放在单事务 runner 内）；记录分步回滚策略，后续前向迁移处理错误索引/规则调整。 |

实现前门禁：确认 H1 code 重取/通知权限、M1 注册失败重试和 M2 唯一时长产品语义；逐项拆分独立迁移与代码提交。对 H1/H2 执行版本兼容矩阵、历史回填/回滚演练和权限测试；对 M2 做重复数据预检、并发创建测试及真实隔离 PG 验证。此草案不代表修复完成或风险已接受。

## D01–D17 与全仓验证状态

本轮只新增 migration 754 的隔离 PG 实测及 R62/F09 源码链路复核，没有完成 D01–D17 的逐域源码、触发条件、期望/实际、回归及外部边界证据矩阵。历史 D 域计划/报告和 F09/F10 定向 race 结果仍不能替代全域续审。全仓 go test ./... 和 race 的既有网络、dispatch 失败未复跑；provider、真实 Redis、部署、96h 方案逐份对照均未验证。

## Git 状态

本记录仅为本地审计文档；未提交或推送。结束时 HEAD 与 origin/main 同为 9bc7ee55c569248963f86d2e7d1f41a329f86d35。本轮并行提交均已保留；web/public/menu-config.json 修改和 docs/audit/todo-state.json 未跟踪文件原样保留。
