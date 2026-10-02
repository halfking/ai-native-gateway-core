# B 会话镜像与IR域子代理报告（窗口：383c4b976..3909d56e0）

> R73 审计轮只读子代理原文。主代理处置：F1/F2/F3（M-6）与 F4（M-3）已修、
> F5 已修（tree body_status 投影）、F6 已修（废弃声明）、F8 维持运维确认项。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| F1 | P2 | **M-6②确认仍成立且被窗口改动加重：Stop() 可长时间阻塞网关优雅停机**。reaper 以 context.Background() 启动；drainWorker 只查 ctx.Err() 不查 stopCh；drain-until-empty 语义把停机阻塞时长从「一批」放大为「全部积压」 | internal/sessionv2mirror/replay.go:307-332、271-283、224-245；cmd/gateway/main.go:2585、7534；cmd/gateway/session_v2_init.go:139 | drainWorker 循环内加 stopCh select → **已落地（R73）** |
| F2 | P3 | **M-6①确认仍成立：worker 无 recover()**。任一 panic 击穿整个网关进程。与 630 reaper 同缺 | internal/sessionv2mirror/replay.go:276-282 | worker goroutine 顶部 defer recover → **已落地（R73）** |
| F3 | P3 | **M-6③确认仍成立：NewTicker「首跳立即」注释失真**。首 tick 在 30s 后；重启积压首排空至多延迟 30s（操作性无害） | internal/sessionv2mirror/replay.go:226-229 | 改注释 → **已落地（R73）** |
| F4 | P2 | **M-3确认仍成立，且两半复合成「死字段+自辩注释错误」**：(a) omitBody=true 时 SQL 投影三列 NULL，随后无条件 classifyBodyStatus 恒 "unavailable"；(b) mergeMeta 不拷贝 BodyStatus，唯一基于真实字节的 BodyStatus 在 merge 中被丢弃。全仓无 meta.body_status 前端消费者（API 契约层谎报） | admin/unified_detail.go:330-336、381、378-380、429/436；domains/requestdetail/locator.go:286-310；admin/unified_detail.go:237-253 | omitBody 时留空 + mergeMeta 补拷贝 + 删自辩注释 → **已落地（R73）** |
| F5 | P2 | **SessionTurnsTimeline 正文缺失横幅在默认路由下永不可现（dead UI）**：时间线消费 /turns（默认 tree 路由），SessionTurnTreeItem 结构体无 body_status 字段，wire 永不发；仅 v2 路由才有 → **已修：tree 端点补 body_present EXISTS 探针投影 + 回归断言（R73）** | admin/session_turns_tree.go:41-48；admin/handler.go:1171；admin/session_turns_unified.go:248-279；web/src/api/sessionTurnsTree.ts:47-53,99-117；SessionTurnsTimeline.vue:124-137,201-208 | tree handler 补投影（已按 unified 同款探针含 <> 'null'::jsonb） |
| F6 | P3 | **死配置 sessions_v2.turn_logs_retention_hours**：spec 已登记但全仓无 Get 消费者；活 TTL 键是 lifecycle.session_turn_logs_ttl_hours。F-15 同类第 5 例 → **已改为废弃别名声明（R73），键保留兼容存量 settings 存储，下一清理轮删除** | settings/spec_sessions_v2.go:174-186；settings/spec_lifecycle.go:69；turn_logs_writer.go:66；bg/partition_manager.go:643 | 删除该 spec 或废弃声明（已取后者） |
| F7 | 观察 | WriteStages 批量化后 turn 的 stage logs 变「全有或全无」：任一 stage EventData marshal 失败整 turn 丢弃（best-effort 可接受，失败半径变大值得知晓） | domains/session/v2/session_writer_v2.go:707-737；turn_logs_writer.go:127-163 | 可选：marshal 失败降级 event_data={} 保留其余行 |
| F8 | 观察 | 指标改名 llmgw_ 前缀去除 + 新增 dead counter；仓内无旧名引用；仓外 Grafana/告警是否已同步无法从仓内验证 | internal/sessionv2mirror/replay.go:101-112；docs/db-changelog.md:738-744 | 运维确认后销账 |

**E6a 口径专项结论（复核通过、无新发现）**：markDead/requeue 的 RowsAffected()==0 只在 lease 交接时走；真 dead 迁移由实际 UPDATE 命中的 worker 恰好计数一次；attempts 无无限重试通道。并行化无泄漏（wg 全 join + SKIP LOCKED + request_id 幂等）。

## 二、核实为健康的面

1. max_attempts spec 一致性（spec 与钳制逐字对齐 + 专防复发守卫测试）。
2. F-16 空批确认修复（drainEmptyConfirmations=2 + 4 个 drain 用例）。
3. turn_logs 单语句原子写 + FIFO + TTL 生命周期三方一致（migration_753_test 钉住）。
4. body_status 两态契约三处一致（E6d 双落地 + 详情侧真字节分类）。
5. 测试真实钉住两态（body_status_test + turn_digest ListEndpoint）。
6. IR 窗口改动健康：非流式 Gemini finishReason 透传保留、Anthropic 同协议流式无损、新增 stop_reason 钉桩。
7. turn_digest/轮次关键文本链路闭环（只抽人读文本、persisted 优先、NullBodies 无 <nil> 泄漏）。
8. session_writer_v2/aggregator/cache_v2_file 窗口改动无回归（primary_request_id 存量优先 + SQL 正则守卫；FileCache TTL 装配断言；摘要剥噪回退）。
9. outbox/writer.go 仅 payload []byte→string（SimpleProtocol hex 根修家族）；当前无生产接线方。

## 三、未覆盖项与原因

- 未运行任何测试/构建（只读纪律）；e2b91fa36 的 -race 门禁未亲验。
- IR 全维度完备性以既往轮结论为准，仅聚焦窗口触碰路径。
- 630 reaper 仅对照确认同缺 recover；自身 Stop 语义未逐行复审。
- 仓外 Grafana/告警改名同步无法验证。
- hook 侧 mirror 注册路径（outbox.go/hook.go/backlog.go）窗口零改动未重审。
- digestDetailColumns 39 列完整列序比对只抽查了 list 面。
