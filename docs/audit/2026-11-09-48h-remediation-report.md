# 48 小时修正审计与三环境发布报告

状态：本轮审计修正与三环境发布验收完成。验收不等于全部存量风险消失，未解决边界见末节。

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

最终审计门按互不重叠两组执行：hollowout 354.496s、其余 ursmcheck 309.171s，均 rc=0；覆盖完整测试集合。发布版本与部署后浏览器结果见后续发布记录。

## 发布中发现并修复的迁移连接缺陷

211 首次发布在迁移门失败，未切流，旧槽 readyz 的 DB/Redis 仍正常。宿主安装了原生 psql，但 `acc-pg17` 被 DNS 解析至 252 公网地址而非本机容器，连接超时。Docker 引擎现显式选择容器通道，继续使用既有 docker exec -i、TCP 127.0.0.1 与 DSN 凭据；容器不可用立即失败，不回退其它数据库。新增运行时回归覆盖坏宿主客户端及不可用容器，9 项通过、1 项 opt-in livePG 未执行；完整 revision 契约门、shell 语法及独立只读审查通过。修复已合并 `998269fb0` 并推送两个 main。

## 三环境发布与运行验收

应用代码统一为 `998269fb`；此后的 `0cadc4f0a` 只增加部署验收脚本/测试及派生台账，不改变服务二进制。该新增验收函数已针对154在役版本实跑通过，不要求为部署机脚本重复发布应用。

| 环境 | 发布后身份 / 活跃端口 | 结果 |
|---|---|---|
| 211 | 2978 / 998269fb / 8781 | canonical deploy-211 rc0；DB+Redis ready；浏览器退出后使用指定管理员重新登录成功 |
| 245 | 2956 / 998269fb / 8781，traffic-only | canonical deploy-245 rc0；DB+Redis ready；脚本登录200、17凭据解密0失败；浏览器重新登录成功 |
| 154 | 2957 / 998269fb / 8782，active | canonical deploy-154 rc0；脚本登录200、17凭据解密0失败；浏览器退出后重新登录成功 |

三台均真实操作营销四页签与表格/卡片切换，检查定价页面及无效公开报价提示。未创建生产订单/优惠码/临时API key，也未声称执行了生产付费推理三协议压测。登录后数据状态初次读取竞态会出现警告，重新检查后消失；已登录接口可用，不能误判为数据丢失。

211 真库：953/954校验和与源码一致，featured索引及trace列存在；5分钟样本81探针中16条持久化trace（并非所有直连探针都会产生网关trace），规范化阶段事件101条。选取其中一条Redis已清理请求，管理API 200、source=postgres、events=6，证明读回路径有效。两次排除探针的完整闭分钟对账分别63/63、61/61。历史全天统计未重建，不宣称全部历史缺口已补齐。

### 154实际事故：单次身份仲裁漏检混合worker

上线后公网readyz10次中6次502；直连新槽10/10为200，目标机nginx路径也有502，排除公网DNS/客户端代理。错误日志逐条指向旧worker10780→旧端口8781；master日志在reload时有 `recvmsg() returned invalid ancillary data`，旧worker仍接受请求而新worker使用8782。事实足以定位混合代际；IPC异常的底层成因尚未确定，不推断为网关Go缺陷。

处置：核对旧worker的PID/parent/comm后发送SIGQUIT优雅退出，不重启整个nginx、不终止新worker。恢复后目标机20/20、公网30/30均200；管理员和页面正常。不是零中断发布：handoff实测17.501s，期间存在真实502。

防回归：`verify_nginx_identity_samples`要求20次独立Connection:close连接、HTTP成功、version/build_seq/git_sha/build_date四字段精确一致；reload后须重做完整20次，否则既有回滚路径执行，旧网关在验收前保留。两个fixture含HTTP失败/旧版本/坏JSON三子场景通过，identity env20/20、pipefail门7.176s通过，独立review无阻塞；新函数在154真机20次验证rc0。20次是有限采样，不等于所有worker永久一致。

回滚：245/154可执行 `bash scripts/deploy-seamless.sh rollback <target>`；211旧槽b容器与镜像保留，回切前先start并检查readyz，再将ns_llm_gateway_upstream回指8783。953/954为兼容性新增字段，应用回滚默认保留列，执行down会丢弃trace，不作为首选。

## 分目标达成与优化优先级

| 子目标 | 达成情况 | 后续优化 |
|---|---|---|
| 主干/WIP/历史分支 | 完成；65提交271文件、11项WIP逐项登记，历史未合并分支0 | 保持每轮固定窗口与来源清单 |
| 业务/授权/统计/迁移修正 | 完成已复现问题与本轮回归 | 价期事务outbox、MCP生产持久化单独设计 |
| 日志/SQL优化 | trace错误车道及legacy22P02根修并验证；未盲加索引 | P1明确preview/body契约；scheduled_tasks先确定owner |
| 三环境发布 | 完成；两处发布实故障已取证修复 | P1调查nginx IPC异常；补跨worker可观测性 |
| 文档/交接/小步推送 | 本报告、AGENTS、handoff和部署账本同步 | 后续从新运行窗口复核，不复制旧绿数 |

## 遗留与后续优化

1. 按量源价与版本账仍为分事务 best-effort；本轮未宣称强一致。后续可用同事务 outbox/版本写路径统一，但先定义计费与历史失败策略。
2. 统计重扫最后完整分钟的悬空键保守迟一 tick 退役，不会永久漏统；若需同 tick 精确清理，拆分聚合与退役上界并补三维实库腿。
3. 旧 bootstrap 申请无 token 哈希会 fail-closed，需重新申请；业务 key `/stats` 仅返本租户审计，进程指标仍由管理令牌查看。
4. MCP FileStore 仅支持单进程写者；上游 AuthSecret 不会被新增明文持久化，现有 JSON store 重启丢失配置的限制仍需专门密钥存储方案。
5. 超过 Redis TTL 的旧 trace 已不可恢复；954 只保证上线后的探针 trace，不能声称补齐旧数据。
6. 新 featured 索引在旧 column census 缺列时被 gap 工具误判为 retired-by-column-drop；这是量具风险，不是生产完成证据。发布验收以实际列/索引/新 trace/校验账为准，后续应要求 DROP 事件证据才能判退役。
7. 新窗口仍有7条 `response_body` JSON丢弃（截断预览）及候选池空/上游失败告警，未宣称日志零错；252的scheduled_tasks参数integer/bigint冲突SQL不在本仓，归属未确定。部署门使用SSOT本地实库账号时出现28P01，候选和基线相同，门判无新增失败；不能把这些实库用例描述为通过。
8. Toolkit 全局 verify 覆盖工作区其它项目，已发现 envs 路径迁移及外项目登记缺口；这些检查不能用本仓 Go 测试替代，也不应把跨仓问题混进网关发布代码。

## 下一轮提示词

从本报告和本轮 handoff 恢复，先核对远端 HEAD、工作树、211/245/154 版本与新日志。优先验证新探针 trace 在 probe_hot 持久化且不再四次重试、统计闭分钟对账，再处理 preview/body 契约和 gap 工具“缺列即退役”误判。遵守先失败复现、最小修正、真实数据库/浏览器验证、小步合并推送；保留已应用迁移字节，不把文档/静态绿当生产证据。

## 归档与提交记录

session append/pin及memora归档成功，memory_id=`69b022a7-7128-4df5-a20f-e7fa1d9832c4`，3条消息/1个pin，degraded=false、vector_ready。原始秘密和日志未入库。应用发布后新增的nginx门仅影响部署脚本，已独立实测。受管integration checkout因app的primary-checkout限制无法archive，保留干净detached并同步最终main；原用户checkout/stash不清理。

本轮共32个非merge提交（含最终文档），每个改动少于200行，最大184行；每步no-ff合入main并推送origin/github。历史分支盘点与本轮任务分支清理分开记录；最终文档之后无应用代码改动。
