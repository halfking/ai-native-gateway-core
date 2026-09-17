# D17 代码卫生 子代理报告(窗口:0a015af51^..b75c91900)

说明:工作树 go 代码干净(仅 VERSION/docs/menu-config 未提交改动),所有 file:line 以工作树亲读为准。

## 一、发现(候选,待主代理复核)

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3(注释漂移,D09 已报,核实成立但方向需修正表述) | R40 豁免注释"走 else 分支按真实不可用处理(含 ladder)"半句不实:豁免命中时确实进 else 并真实写不可用,**但 ladder 不升级**(gatewaySide 判定不带豁免,backoff 钉固定 15 分钟,cf 冻结)——与同文件 :253-256 自相矛盾,且注释放在"非豁免"分支体内,阅读方向相反 | bg/node_probe.go:2091-2093(注释)、:2085(guard)、:2099-2104(else 真实写)、:2189+:2195-2196(固定 15m)、:2202(cf 冻结)、:253-256(const 注释);队列同构 :462/:843-845+:851 | 只改注释:删"(含 ladder)"或改为"绑定/观测面走 else,ladder 仍冻结" |
| 2 | P3(注释漂移) | node_probe.go 文件头 backoff 阶梯文档双重过时:(a) "attempt 7+→ +24h" 实际链 2026-07-24 起封顶 6h;(b) "row is marked paused / worker stops probing" 实际全 bg 包已无 paused=TRUE 写点,attempt 7+ 行以 6h 永续 tick。文件内自相矛盾(:1053、:2802-2803 写 6h 封顶,:1307 仍写 "up to 24h") | bg/node_probe.go:21-24(头注释)、:1307;对照 bg/probe_backoff.go:75-88、:822 | 头注释改 6h+永续 tick;:1307 一并更正 |
| 3 | P3(注释漂移) | SetModelQualityTrigger 文档称回调在 consecutive_failures reaches nodeProbeMaxAttempts(=7) 时触发,实际调用点阈值为 attempt >= 2 | bg/node_probe.go:442-443(文档) vs :2226(调用) | 文档改 attempt>=2 |
| 4 | P2 候选(行为面请 D09/主代理复核)+P3(注释) | 启动清扫与 R40 豁免互相打架:deescalate 每次重启把 probe_endpoint_build/probe_request_build 无条件恢复 TRUE 并拉回 next_retry_at,而 R40 豁免后单凭据密文损坏会合法新写同一 reason——清扫每次重启把豁免成果抹掉。注释"(the pre-guard binary's writes; the guard refuses new ones)"已失实 | bg/node_probe.go:578-580(过时括注)、:587-594、:604-613;对照 :2849-2852、:2099-2104 | 登记待清:豁免写打独立 reason 标记或清扫排除 decrypt 形;本轮至少先修 :578-580 注释 |
| 5 | P3(重复分支) | KindClientBug case 四个赋值与既有 case 逐字相同仅多注释,可合并进 case 列表(保留注释) | errorsx/failover_policy.go:117-128 vs :112-116 | 合并,go test errorsx 钉住 |
| 6 | P3(注释漂移) | durable/rls.go 头注释称 settlement_intents 也是两分 policy 形状;实际 520 已合并为单条 durable_task_settlement_access(三分 OR)。两分结构仅对其余三表成立 | durable/rls.go:3-5、:14;对照 520:44-55、722:74-99 | 头注释分句表述 |
| 7 | P3(登记项) | 迁移 721 缺 .down.sql,相邻 719/720/722/723 均成对;只加列可逆性无疑问,疑漏登记 | sql/migrations/startup/(721 只有 up) | 按纪律核对 down 是否强制;是则补并走 revision-sequence |
| 8 | P3(待清清单,只登记不重构) | 冗余候选三项:(a) 余额五点元数据写碎片在 3 写点重复、manual 谓词 2 写点重复(guard 侧无指回注释);(b) durable 族 "begin tx+defer rollback+GUC" 4 行前奏约 8 处重复可收敛 withBypassTx;(c) probe_service 外层 guard 与 updateBindingAvailability 内层 guard 是必须同步的同形对(值得互指注释) | admin/provider_credential_balance.go:108-115、bg/balance_floor_guard.go:857-860+:1001-1005、bg/credential_probe_v2.go:576-590;durable/settlement_outbox.go:131-136 等;bg/probe_service.go:712-716 vs bg/node_probe.go:2852 | 登记轮文档遗留;标注级处置 |
| 9 | P3(流程卫生) | R41 双胞胎提交 e9d46b37e/6276a3ff9 同题同 diff 同刻入库,双胞胎会掩盖"第二个提交没人真正看"的审查空洞 | git show 两 commit(同 patch,blob 4b5346cc3) | 轮文档记教训:push 前 git log --since='5 minutes' 自查(conventions §7 候选补充) |

## 二、核实为健康的面

- **R41 双胞胎合并产物无重复**:builder 仅一份+唯一调用点;测试无重名;两父 blob 同。
- **同类 Sprintf 腐蚀无残留**:其余 frag 拼接均为裸串拼接无 Sprintf 包裹。
- **failover_policy 其余 case 无重复**;classify.go MiniMax 追加 pattern 带负例测试,注释与实现一致。
- **RESERVED 标注齐全**:lan_advertise 三处;NewLANAdvertiser 已接线;main 仅一处 signal.NotifyContext;ProviderTemplatesProvisioned/IsSchemaMismatchError 均有调用接缝——窗口新增导出符号无零调用接缝。
- **注释诚实化先例良好**:credential_recovery.go 主动更正;shadow_actors.go 如实记录双侧约定;R41 修复注释完整记录病灶与先例。
- **durable GUC 包裹改造机械一致**:单语句走 helper、多语句手工 tx+GUC,逐处带出处注释,无漏包迹象。
- **721 余额元数据写点口径一致**:admin PATCH 置 manual+清 error,三个 api 写点统一,admin 即时刷新按注释刻意覆盖 manual(设计如此)。
- **admin/credential_success_rate.go R38 实修无复制残留**:旧内联 DELETE 已删。

## 三、未覆盖项与原因

installer main.go/runner.go、domains/session/v2 测试改名、web 前端(非本轮派发重点未逐行);新增测试仅查函数清单未逐断言(占坑 goroutine 模式未发现);vendor/ 依赖不审;发现 4 的实际行为影响面需真库验证。
