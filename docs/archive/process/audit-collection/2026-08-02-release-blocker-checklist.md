# §13 发布阻断条件核对报告

> 核对日期：2026-08-02
> 核对基线：origin/main @ `bc5c469d`
> 核对方式：只读代码审计 + spec §11.1 完整 race 基线（30+ 子包全部 PASS）

## 核对结论

8 项 PASS、2 项 DEFERRED（显式声明延期）、1 项 BLOCK（需修复才能发布）。

## 逐项核对

| # | 阻断条件 | 结论 | 关键证据 |
|---|---|---|---|
| 1 | P0/P1 数据丢失/协议错误/终态回退/重复 turn/真实 data race（含 C-1） | **PASS** | L-2 logs guard（client.go:995）+ WAL guard（request_logger.go:630）；SetTerminal CAS（pipeline.go:527，EmitFailure/RateLimited 已接入）；turn ON CONFLICT 回读（turn_writer.go:106）；C-1 authoritative 下 StateManager 零调用（state_backend.go:146 + router.go:239 guard）；`-race` 0 DATA RACE |
| 2 | 无法用 request id 关联客户端/上游/主记录（含 G-ID-2） | **PASS** | handler.go:288 request_id 回写；middleware.go:31 X-Gw-Session-Id 优先（G-ID-1）；四入口均写 X-Request-Id 响应头 |
| 3 | tools/扩展字段静默丢失（F-1/F-2/F-3） | **PASS** | F-1 types.go:381；F-2 Extensions（types.go:161）；F-3 anthropic_to_chat_request.go:305 |
| 4 | 流式 tool 参数仍可作为非法 JSON 发出（F-5） | **PASS** | tool_arguments_assembler.go + json.Valid；anthropic_bridge.go:620 content_block_stop 终帧校验，非法即 emit error chunk 并中断 |
| 5 | 错误信封与客户端协议不一致（E-1） | **PASS** | writeErrorAnthropic（handler.go:5646）输出 `{"type":"error",...}`；isAnthropicMessagesPath 分发 |
| 6 | authoritative 路径 tenant 不正确或旧状态源参与健康判定 | **PASS**（条件性） | router.go:143 seed 携带 TenantID；URSMv2Backend no-op（state_backend.go:53）；生产默认 URSM_V2_MODE=off，authoritative 未上线（spec §4.4 允许迁移态 legacy） |
| 7 | 队列满/批失败/shutdown 导致已接受事件不可重放（含 L-2） | **PASS** | 队列满 fallback（request_logger.go:400）；flushBatch 整批 fallback（511/531/554）；Stop 幂等+drain（686）；主 shutdown 顺序（main.go:4494） |
| 8 | 真实 PostgreSQL/Redis 故障注入未通过 | **BLOCK** | 所有 PG 故障测试用 pgxmock（SQL mock）；所有 Redis 测试用 miniredis；唯一真实 PG 集成测试为 happy-path 不注入故障；spec §11.1 要求事务中止/Redis kill/重放 |
| 9 | 关键协议 fixture 与官网格式不一致 | **DEFERRED** | golden fixture 仅覆盖 anthropic↔openai；缺 Gemini/Responses golden + 官网 schema 比对 |
| 10 | routing_state_source 缺失或 fail-open 无占比可观测（S-3） | **DEFERRED** | 枚举+计数完整（statesource.go）；但 Snapshot() 仅测试读取，无 Prometheus metric 暴露 |
| 11 | §3.1.1 八类关注点任一未覆盖且未声明延期 | **PASS** | 八类均有代码覆盖；已知 GAP 已在审计报告中声明延期 |

## BLOCK 项详情（#8 真实 PG/Redis 故障注入）

当前所有数据库故障测试使用 mock：
- PG 故障：`pgxmock`（request_logger_flushbatch_test.go 等），不经过真实 PostgreSQL 事务语义。
- Redis 故障：`miniredis`（manager_lru_test.go 等），`mr.Close()` 模拟 kill。
- 唯一真实 PG 集成测试（`tests/integration/request_lifecycle_test.go`，`//go:build integration`）为 happy-path，不注入故障。

spec §11.1 明确要求：
> 带真实 PostgreSQL/Redis 的 integration tests，覆盖事务中止、队列满、Redis kill、重放和重复 request id。

## DEFERRED 项声明

### #9 协议 fixture vs 官网 schema
- golden fixture 仅覆盖 anthropic↔openai 两个方向（`testdata/ir_golden/`）。
- 缺 Gemini、OpenAI Responses 的 golden round-trip fixture + 官网 schema 比对。
- spec §7.2 tools 验收矩阵的 fixture 化比对未完成。
- **延期理由**：IR 层 round-trip 测试（integration_roundtrip_test.go）已覆盖所有协议的 Parse→Serialize→Parse ID 稳定性；golden fixture 化属于增量验证，不阻断当前发布。

### #10 routing_state_source 占比可观测（S-3）
- 枚举 + 原子计数 + Snapshot API 完整（statesource.go）。
- 全链路记录已接线（router.go outer + manager.go inner）。
- 但 `Snapshot()` 仅被测试读取，无 Prometheus collector 暴露 `/metrics`。
- **延期理由**：生产默认 `URSM_V2_MODE=off`，S-3 的 fail-open 占比在 authoritative 切换后才有意义；Prometheus 暴露是 5 行代码增量，可在切换前补齐。

## 补充说明

- 生产默认 `TRANSPORT_LAYER_IR_ENABLED=false`（IR 未默认）、`URSM_V2_MODE=off`（authoritative 未上线）。
- spec §1 所述「今日 live 默认仍是 Legacy」未变；F-1～F-5/E-1 的 IR 路径代码已就绪但生产未走 IR。
- 切换前需同步更新方案与验收矩阵（spec §1/§14 已要求）。
