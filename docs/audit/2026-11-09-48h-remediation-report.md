# 48 小时修正审计与三环境发布报告

状态：执行中；本文件不构成全量验收通过声明。发布结果待实测回填。

## 范围与来源

- 固定审计窗口：2026-11-07 12:56:13 → 2026-11-09 12:56:13（Asia/Shanghai）。
- 初始 main：`bde2baa598`；fetch 后 ff 合并 `1fa2ea592`，43 文件，无 WIP 重叠。
- 窗口内 65 个非合并提交、271 个文件；逐提交与逐文件来源见 [清单](evidence/2026-11-09-48h-commit-file-inventory.md)。清单是范围证据，不代表所有路径均做过动态验证。
- 初始 WIP：10 tracked + 1 untracked；保存原 patch、status 和文件备份后复核接纳。未删除原有两笔 10 月 stash，也未动 `/private/tmp/lgw-probe/cursor` detached checkout。
- 分支盘点：本地只有 main；origin/github 的 `ls-remote --heads` 都只有 main。近一周且超过一小时不活跃的未合并子分支 **0**，无分支可逐文件合并或删除。本轮新建工作分支不计入历史清理对象。

## 按目标批判与修正

| 目标 / 原实现承诺 | 批判及事实证据 | 本轮处置 |
|---|---|---|
| F1 探测恢复与 429 节奏校准 | 已有两端范围豁免、配额恢复通知、窗口冷却和加权路由；不能仅因探针队列 success 就认定发生直连探测 | 保留实现，跑 bg/credential/executors 回归；运行效果需部署后新窗口验证 |
| F2 迟到统计整桶重扫 | `now-lag` 含秒，而 SQL `ts <= until` 是整键覆盖；真实 PG 证明已完整分钟 2 条被重扫截成 1 条 | 只重扫完整闭分钟，截止至边界前 1 微秒；补迟到行、边界行、重复重扫行为测试 |
| F3 一个模型/模式一现行价且价格期连续 | 参考价的唯一写目标发生数据库错误仍返回 nil；读源价失败被误作删除；空现行行并发不受行锁保护；前闭后开分别取时产生空档 | 主写错误上抛；附属快照保留 best-effort 但读失败不闭期；模型×模式事务锁；前后同一时间点；真实 PG 并发/失败/连续期验证 |
| F4 营销赠送必须受预算约束 | gift 与 credits 同样发钱包，但预算守卫仅检查 credits；实跑第二个租户仍可超发 | 两类共用预算检查，拒绝负预算/负累计，减法避免加法溢出；拒绝时钱包与 ledger 不变 |
| F5 首购与支付确认链 | 确认 SELECT 漏 `expires_at`，零时间使 pending 全被判过期；仅建单查首购挡不住两张未支付单 | 补读过期时间；确认阶段钱包锁串行、复查首购；并发只成功一次，重复确认幂等，普通后续购买可用 |
| F6 MCP 控制面租户治理 | scope 存在不等于对象 owner/委派约束生效；原实现可跨租户写与审批、register 覆盖已有对象、审计泄漏、受限 key 签发更宽权限 | HTTP 行为回归后逐路径收口 owner、创建与管理、完整委派包络；精确删除不连带 shared 对象 |
| F7 MCP 数据面与领取/吊销 | 停用后仍能直呼；同名租户缓存串用；触碰使用时间可覆盖吊销态；bootstrap 未核领取 token、并发能重复领取 | 停用执法、租户缓存键、单字段原子 touch；领取令牌哈希+锁内一次消费；审批/请求身份最终调用处核验 |
| F8 953 迁移可交付性 | 仓库 COMMENT 拼接语法为 42601；211 虽有列与 953 记录，但 checksum=0，且部署 files 无 953，不能据此宣称仓库 SQL 已执行 | 修语法、事务/锁/语句预算、up/down、自记账、部署清单、installer、objects、契约与 db-changelog；未改任何已登记 checksum；隔离 PG 两次完整重放通过 |
| F9 探针 trace 的完整落盘 | 211 两小时内 363 个请求各失败重试 4 次，产生两组各 1452 条告警；ID 对拍业务 hot/cold=0、probe_hot=363 | 追加 954 nullable JSONB 列，trace 第三读写腿；规范化事件与 envelope 持久化后才删 Redis；开关关闭仍能读历史；真实 PG 回归 |
| F10 前端国际化质量 | 全量 Vitest 暴露新增硬编码中文 4 处、de/es/fr 未本地化及 ja/zhTW 同源指标上涨；旧“全绿”结论不能沿用 | 提取营销表头与价格单位两键×8 语种；完善相关页面上下文译文；不抬 baseline。人民币价格单位明确到分/0.01 CNY |
| F11 派生台账可复算 | admin routes、SQL 读点、installer reachability、db expect、文档行号/pipefail 候选数未跟代码走 | 按生成器机械重生，计数 98→100 对应既有候选表新增两个真实位置；不删判据；保留旧生产采样时间，不伪装新采 |
| F12 legacy 探针审计必须能写 JSONB | 252 有 65 次 22P02，245 日志明确同型 node_probe_runs 写失败；统一路径已用 JSON helper，但 legacy runOne 仍把空 headers 写空字符串 | 真正 runOne→PG 先红后绿；无条件复用 probeHeadersJSON，空头写 `{}`；专用 DSN 三面登记并限制所有 fallback hosts 为 loopback |
| F13 部署锁元数据与 SSOT 同口径 | 共享库已新增不可被逻辑标签覆盖的 source_hostname，本仓 writer 漏同步，锁一致性门持续红 | 新增实际 hostname；行为断言 SOURCE_HOST 覆盖不影响机器身份；锁契约和 parity 通过 |

## 初始本地修改逐项去留

| 初始文件组 | 判定 |
|---|---|
| `cmd/tools/sql_source_indirection_audit/manifest_test.go` | 新增 probe_model_scope 读点登记合理；canonical 桶扩大不缩小；测试复核，订正未定义列 SQLSTATE 注释 |
| `mcp/control.go`、`metrics.go`、`server.go` | uptime 从未写值修为构造/健康端点更新，保留；本轮另修上述实际授权与数据隔离缺陷 |
| `mcp/server_test.go` | 保留真实脱敏夹具的运行时拼装与 uptime 数值判据；未把伪 secret 加扫描豁免 |
| canonical 读点三份产物 | 初始为行号漂移；本轮 stats 修复后重新提取，防止保留再次过期的产物 |
| pipefail candidates TSV | 对生成器逐字重算；两条计数基线遗漏同步一起纠正 |
| D17 settings wiring | 删已接线 approval_rate 登记，三档系数只在前端快填的服务端无读者说明保留；不放宽扫描判据 |
| cron 双仓同步轮草稿 | 保留为前轮证据；它的测试/未部署结论属于当时窗口，不能充当本轮发布证明 |

## 运行日志与 SQL：事实、归因、优化

- 样本：211 当前蓝绿在役槽 b，部署前版本 `2977 / 1179dbee-dirty`；应用日志 30,899 行，PG 2,970 行，取最近两小时；原始日志仅保存在仓外，未提交业务 payload/DSN/token。
- trace 写回失败事实与根因见 F9。优化是把请求写回实际所属车道，消除每请求四次必败的 UPDATE/重试，不能只降低告警级别。
- PG 样本 137 条慢语句。最长 128s 是备份 COPY，约 19–20s 的 `jsonb_each(to_jsonb(r))`/大正文去重是并行诊断查询，不能冒充正常业务慢 SQL。建议诊断先缩请求/时间窗口、只投影需要列，并把备份与交互验收错峰；本轮不为这些查询盲目加生产索引。
- `pg_stat_statements` 在 211 未被 shared_preload_libraries 加载；统计扩展存在不等于可用。本轮采用服务器慢日志，未为采样重启生产 PG。
- 339 条 telemetry JSON 丢弃早于新 executor 预览提交：旧探针把响应截为 500/512 字节后当 JSONB 正文写入，非完整 JSON 被正确拒绝。该问题登记为 preview/body 契约债，未把截断 JSON 强行包装成完整响应。
- 953 索引预算实测：211 PG17，1,769 行 featured 临时投影，CREATE INDEX 1.166ms，事务回滚；锁等待 2s，保守语句预算 10min。

## 测试与部署证据

已取得的新鲜本地证据：

| 命令 / 范围 | 结果 |
|---|---|
| `CGO_ENABLED=1 go build ./...` / `go vet ./...` | rc=0 / rc=0 |
| `go test $(go list ./... | rg -v /scripts/ursmcheck$) -count=1 -timeout=300s` | 361 包通过；慢审计包单独串行 |
| `go test ./scripts/ursmcheck -count=1 -timeout=1200s` 首轮 | 完整结束 754.196s；暴露前端/部署锁读方与 post 台账未同步，未标假绿 |
| stats / MaaS / trace 新真实 PG race 回归 | 旧实现失败、修后通过；trace 独立复跑 1.836s；MaaS 六腿及统计两腿通过 |
| legacy runOne 空头审计真实 PG，race ×3 / bg 全包 | 1.856s / 35.411s，通过；非 loopback fallback DSN 被负控拒绝 |
| MCP 三层 harness | 47/47，通过，含 Maestro 真实浏览器与管理令牌指标保留验证 |
| 953/954 契约、隔离 PG 重放、installer 全包 | 通过；954 首次/重放不改原行 relfilenode，down 对称 |
| `pnpm test` / `pnpm typecheck` / `pnpm build`（web） | 298 文件 / 3034 测试通过；1 文件 / 2 测试既有跳过；类型和构建 rc=0 |
| 前端七类底层质量门、锁 parity/contract、diff --check | 通过；新 compact 卡片完整字段与动作，基线未抬高 |

最终 hollowout 回归、发布版本与部署后浏览器结果仍待回填，不据局部门声称整体完成。

## 遗留与后续优化

1. 按量源价与版本账仍为分事务 best-effort；本轮未宣称强一致。后续可用同事务 outbox/版本写路径统一，但先定义计费与历史失败策略。
2. 统计重扫最后完整分钟的悬空键保守迟一 tick 退役，不会永久漏统；若需同 tick 精确清理，拆分聚合与退役上界并补三维实库腿。
3. 旧 bootstrap 申请无 token 哈希会 fail-closed，需重新申请；业务 key `/stats` 仅返本租户审计，进程指标仍由管理令牌查看。
4. MCP FileStore 仅支持单进程写者；上游 AuthSecret 不会被新增明文持久化，现有 JSON store 重启丢失配置的限制仍需专门密钥存储方案。
5. 超过 Redis TTL 的旧 trace 已不可恢复；954 只保证上线后的探针 trace，不能声称补齐旧数据。
6. 新 featured 索引在旧 column census 缺列时被 gap 工具误判为 retired-by-column-drop；这是量具风险，不是生产完成证据。发布验收以实际列/索引/新 trace/校验账为准，后续应要求 DROP 事件证据才能判退役。
7. Toolkit 全局 verify 覆盖工作区其它项目，已发现 envs 路径迁移及外项目登记缺口；这些检查不能用本仓 Go 测试替代，也不应把跨仓问题混进网关发布代码。

## 下一轮提示词

从本报告和本轮 handoff 恢复，先核对远端 HEAD、工作树、211/245/154 版本与新日志。优先验证新探针 trace 在 probe_hot 持久化且不再四次重试、统计闭分钟对账，再处理 preview/body 契约和 gap 工具“缺列即退役”误判。遵守先失败复现、最小修正、真实数据库/浏览器验证、小步合并推送；保留已应用迁移字节，不把文档/静态绿当生产证据。
