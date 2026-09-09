# ADR: IR 传输层(TransportFactory/IRTransport)整体下线

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** `domains/transformation` 传输层工厂与双实现;`domains/streaming/response_format_adapter.go` 死代码;`TRANSPORT_LAYER_IR_ENABLED` 配置面
- **Origin:** 2026-09-09 24h 审计第三轮 P1 #1(Track F 决策批次)
- **Retirement commits:** `bfea15b40`(transformation)、`a37f53ba2`(streaming)、`aab18f9e4`(config/docs)

## Context(背景)

spec §10.4.1 曾规划"IR 作为协议转换默认路径",并落地了灰度工厂:

- `TransportFactory`:按 `TRANSPORT_LAYER_IR_ENABLED` / 租户白名单 / 模型白名单 / 百分比在 IR 与 Legacy 两个 `TransportLayer` 实现间选择
- `IRTransport`:`internal/ir` Parse → Serialize 的四象限转换 + `ConvertStream` 流式转换
- `LegacyTransport`:复用 `transformation/anthropic` 回调的备选实现

2026-09-08 第二轮审计纠偏、2026-09-09 第三轮审计复核均确认:**全仓无生产调用点**。
生产请求/流式路径由 `cmd/gateway/main.go` 接线的 `domains/streaming/executors`
(活跃桥)+ `domains/transformation/ir_converter.go`(TransportIRConverter,请求方向
IR 转换,`main.go:1537`)+ `domains/transformation/anthropic` 桥承担,不经过该工厂。
`docker-compose.yml` 中的 `TRANSPORT_LAYER_IR_ENABLED=true` 无任何生产代码读取,
属无效配置;此前落在 `SerializeAnthropic`/`processStreamLine` 上的"流式 IR 修复"
(如 A#2/6ebd5f30c)实际全部是 test-only 路径,造成修复假覆盖。

## Decision(决策):下线,不接线

按本批次决策原则评估两条路:

### 接线路线的改造面(否决)

| 评估点 | 结论 |
|---|---|
| 生产调用点在哪接 | 唯一可接入点是 `domains/streaming/executors` 的流式分支(`executor_chat.go` Q2 分支等)——这正是活跃桥热路径,违反"不动热路径"约束 |
| 需触碰的文件 | executor.go / executor_chat.go / executor_anthropic.go / main.go 接线闭包 + 工厂自身 ≥5 文件,且 IRTransport 需补齐 executor 已有能力(attempt gate、pending capturer、StreamCapture、遥测、重试)才有等价语义,实际 >5 |
| 前置修复 | SerializeAnthropic 三处(message_start 前置、content_block 生命周期、signature_delta)+ InternalResponse StopReason/StopSequence 两字段——默认关闭时依旧零生产覆盖 |
| 回归风险 | 在 72s 流式测试热路径上引入旁路实现,收益为零(默认关闭)、风险为实 |

### 下线路线的边界(采纳)

**删除**(全部为零生产引用的死代码或其专属测试):

- `domains/transformation/`:factory.go、layer.go、ir_transport.go、legacy_transport.go、detector.go、metrics.go(`transport_conversion_*` 指标仅被死路径递增,恒为 0)、fixture.go + `testdata/ir_golden/`
- `domains/streaming/response_format_adapter.go`(审计 R3 结构清理清单 #11,本轮确认零引用)
- 测试:factory_test、factory_switch_test、stream_test、integration_test、cross_protocol_stream_test、ir_transport_test、legacy_transport_test、legacy_transport_stream_test、roundtrip_test、fixture_test、ir_stream_anthropic_fields_test、response_format_adapter_test、`tests/integration/ir_default_switch_test.go`
- 配置:`docker-compose.yml` 与 `tests/lib/fake-remote-host.sh` 中的 `TRANSPORT_LAYER_IR_ENABLED`;`domain/README.md` 死文档

**保留**(grep 确认的活跃消费者,一律未动):

- `internal/ir` 全包:InternalRequest/InternalResponse/StreamChunk 与全部 parser/serializer。
  活跃消费方:executors(请求方向 IR 转换)、dispatch、attachments、hooks/audit 等
- `domains/transformation/ir_converter.go`(TransportIRConverter):生产请求转换主路径
- `domains/transformation/circuit_breaker.go`:TransportIRConverter 的按 provider 熔断在用
- `domains/transformation/extension.go`:IRExtensionExtractor/Restorer 被 ir_converter.go 在用
- `domains/transformation/anthropic` 子包:活跃桥(`anthropic_bridge.go`/`responses_bridge.go`)在用
- `(ir.StreamChunk).SerializeAnthropic` 及 `testdata/anthropic_stream_golden`:删除两个死调用点后
  仅剩库内单测/golden 消费,作为库代码保留(零运行时成本,Go 不链接不可达方法)

## Consequences(后果)

1. 消除"两套并行流式实现"的维护负担与 test-only 修复假覆盖的根因。
2. `TRANSPORT_LAYER_IR_ENABLED` 从配置面消失;已部署环境中该变量变为无害的未消费项,可随下次发布清理。
3. 已知遗留(随下线一并接受,不再修复):
   - `internal/ir` 的 StreamChunk.SerializeAnthropic 缺 message_start 前置缓冲、content_block_start/stop 生命周期、signature_delta 透出;
   - `ir.InternalResponse` 缺原生 StopReason/StopSequence 承载字段,Anthropic→IR→Anthropic 往返 stop_reason 有损(经 OpenAI 归一化映射)。
   两者仅影响未来若重新接线路径时的保真度,对现行生产路径无影响(现行桥为手写 bridge,不经过这两个函数)。

## 恢复路径(如未来需要 IR 流式转换)

1. 从本 ADR 对应的提交(`bfea15b40`^)恢复 factory.go/layer.go/ir_transport.go/legacy_transport.go 及其测试;
2. 先在 `internal/ir` 补齐 SerializeAnthropic 三处(golden 用例 `testdata/anthropic_stream_golden` 仍在,可直接 diff 校验)与 InternalResponse 两字段;
3. 在 executors 流式分支挂工厂调用点——此时应作为独立的、带完整对照测试的专题改造评审,不走灰度开关暗改热路径。
