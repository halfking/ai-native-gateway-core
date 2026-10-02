# D14 脱敏/压缩链子代理报告（窗口 e3406f9e2..77837b013 + 3483152cb 补漏）

窗口内六目标目录代码承载提交只有 3483152cb（约 2470 行）与 77837b013 的 reasoning 修复（33 行）。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 | 处置 |
|---|---|---|---|---|
| 1 | 中 | detector.go deduplicateFragments 确认死代码（旧语义被 mergeFragmentSpans/outsideReservedSpans 取代） | detector.go:242-254 | 本轮已删 |
| 2 | 中 | IPv4 规则误伤版本号形态为真：sensitive_patterns.yaml 删了 exclude 白名单+netip 过滤，1.2.3.4 合法 IPv4 会过；输出侧被换成 [REDACTED]、五段式部分匹配、缓存体击穿回路 | operational_patterns.go:19 / detector.go:74-82 | 登记取舍；补形态用例待办 |
| 3 | 中 | credential 键名硬阻断不可配置：模型新生成 {"username":"bob"} 即整响应拦截；credentialField 宽键名硬编码；唯一开关只影响 detector span 的 mask/block | output_sensitive.go:62-63,90-103 / input_tools.go:58-64 | 拍板项：降级 mask 或配置化白名单 |
| 4 | 低 | installSmartSaniGuard restore 构造失败提前 return，输出 gate 与 restore 一起失装（可达性极低，语义 fail-open） | goal_control.go:560-565 | 登记即可 |
| 5 | 低 | cachedBodyPassesGuard 全叶遍历造成"永久缓存击穿"回路（id 含 sk-/ak- 形态则该 session 每轮全量重压缩；仅性能） | generated_text_guard.go:29-86 | 登记 known cost |
| 6 | 信息 | 1MiB 流式暂存上限是长输出可用性悬崖（mandatory gate 恒启用后从可选变全量） | stream_compliance.go:27,153-156 | 拍板确认+监控拦截率 |
| 7 | 信息 | holder 释放时 InjectAfter 语义收窄（与提交注释意图一致） | chain.go:193-205 | 确认无旧语义依赖 |
| 8 | 信息 | encodeSessionStateFields marshal 失败跳过字段为理论缺口（纯结构体实际不失败） | session_cache.go:770-800 | 可忽略 |

## 二、核实为健康的面
- 占位符撞号/并发三层防护（进程内键控引用计数锁/Redis SETNX 租约+Lua 原子提交/先预留后分配）逐行核实
- cache lineage 16 字节 generation：输入/还原/压缩三侧全校验，memo key v2 混入 generation
- 输出侧 gate 真接线：生产链 [goal, audit, guard, restore, ocHook]，wiring 测试与装配一致
- 流式/非流式对称：增量帧扣留至终态、分片 tool arguments 拼接复查、FailClosed 双路径生效
- generated_text_guard 不会误判供应商正常输出（仅作用于摘要产出点+缓存体复查，opaque 键跳过）
- sessionv2mirror 镜像失败不阻断主链路（EnqueueMirrorFailure 三级降级）
- Gemini 经 IR 合成 chat 请求穿 sanitize 中间件，无绕过
- detector 并发：读写锁+模式快照+RE2 线性时间

## 三、未覆盖项与原因
未执行测试（只读纪律）；多进程租约竞争未做双进程实伤验证；streaming/handler.go 重试与 survival 线不在窗口 diff；288+174 行新测试仅核对关键断言强度；mandatory gate 性能包络未量化。
