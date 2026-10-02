# R92 域C 报告：完整性指纹检测器、告警规则、telemetry、client_ip 写入方（第三十二轮）

审计人：分域子代理 C（只读）。HEAD=8f79ad402。声明：审计中出现的 3 修改+3 未跟踪文件（dispatch/executor_chat 在途改动）系并行会话（本轮主代理修复），未纳入判定。

## 一、逐提交判定

| 提交 | 判定 | 证据 |
|---|---|---|
| ba861a35a §9.50 | 成立（机制），因果被 §9.51 自我推翻 | 4 处接线与 diff 逐一吻合（drift.go:109,176,188,211）；3 指标+2 告警落地 |
| 9608d317d §9.51 | 成立 | 真库复现三面非空全 0（request_logs 2,166,379/0、session_turns 1,684,485/0、model_integrity_events 7,094/0）；纯文案无行为变更 |
| 33c238341 §9.52 | 成立 | arm 移至门控外：telemetry/client.go:1289/:2077 在 `if logsWrite` 之前调 observeSystemFingerprint；persistSystemFingerprint（:2556-2566）不再 arm；结构门三环+blockCallsDeep 分离真实有效 |
| a0da9066d 指纹腿 | 成立 | 两处「永久关闭」旧断言按 §9.52 订正+残余限制补记；yml 双分支读法在位（:115-118）；「816 SQL 本体不动」SHA1 属实（78d8f3ff 前后一致） |
| a0da9066d 两条死/噪告警根修 | 成立（经变异验证） | 正向 6 场景 promtool SUCCESS；另做 3 组反向变异（恢复旧 changes()>0 / 去 on(family) group_left / 去 CohortEmpty 活动守卫）场景 1/3/6 分别变红 |
| a0da9066d client_ip ParseIP 门 | 成立（写路径正确），读侧缺口见 P2-2 | 四场景钉测绿；OriginMiddleware 在全局链（main.go:7201）使两条 ungated fallback 实际休眠 |

## 二、发现清单

**P2-1（观测盲区，已文档承认无告警覆盖）**：停写期 arm 触发后每轮 fingerprintScanRun 照跑、告警全静默，但 scanDrift 只读 request_logs_with_current_month（drift.go:259-262）→ 空扫。「arm 恢复 ≠ 检测恢复」三处如实记录且 yml 明示属产品决策——但无指标/告警能区分「扫活面」与「扫冻结面」。

**P2-2（816 读侧守卫不足，写侧门只盖主路径）**：`'deadbeef'` 过正则、`::inet` ERROR；ParseIP 门盖主写链（origin_mw→request_meta.go:335→entry/session mirror→session_turns.client_ip→816 投影）；未来任何绕过 origin_mw 的写入方重新引入投毒，读侧正则本可廉价加固而刻意不动（SHA 纪律）——与域A P1-1 同根，正解=817 收编。

**P2-3（运营噪声，刻意为之）**：BgFingerprintDriftNeverScanned（critical）当前稳态每次重启 ~2h10m 后永久 firing（上游从不发 X-System-Fingerprint，短路是稳态）；SkippedNoScan 每小时 re-fire。yml 自声明「如实记录空检测能力」。

**P3**：P3-1 last_scan_unix 语义过载（可为进程启动时刻，drift.go:105-109，文档已声明）；P3-2 session_turns.raw_model_name 接线已活（s1a_fields.go:101；真库 3,744 行非空自 10-01）但尚无活读方（「有写无读」）；P3-3 指纹两条告警无 promtool 单测（time() 墙钟难钉）。

## 三、核实为健康的面
双臂行为矩阵自洽（两 skip 路径都 recordFingerprintDriftSkip、run 路径都 recordScan，结构门钉住同生共死）；arm 粘性不抖动；「检测器关闭」态本身有告警覆盖（NeverScanned 持续态+SkippedNoScan 事件态分工双钉）；3 指标无标签（GW-00 基数 1）且 metric↔alert 一一对应有门；telemetry 40 行改动零风险（恒空⇒零开销）；settle 告警标签配对 promtool 验证；build/vet 五包绿。

## 四、不确定项
健康模式「不响」无法以真实 unix 时钟在 promtool 等价复现（算术直推+未来时间戳佐证）；本机 settings_kv 无停写键（默认 true，非停写态）；252 停写键值未验证；raw_model_name 数据落库早于接线提交（推测本地二进制先于 commit 构建，不影响代码判定）。
