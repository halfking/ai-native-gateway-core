# ADR-0003: requestfact 生产接线与 payload hash 版本化

- **Status**: Accepted（Track G，2026-09-09）
- **Scope**: `internal/requestfact` metadata projection、canonical payload hash、生产 handler 接线
- **Related**: `internal/requestfact/codec.go`、`internal/requestfact/metadata_projection.go`

## Context

`InjectGatewayMetadata` 与 `ProjectRequestMetadata` 已完成契约和单元测试，但当前没有稳定的生产调用点。网关请求生命周期仍由 `domains/streaming` 的协议专用 handler、executor 和 request-log pipeline 驱动；在没有终态 `CanonicalRequestFact` builder、完整 response/upstream IR 和统一终态提交点前，把 metadata projection 接到某一个协议 handler 会产生协议不对称、重复构建或半成品事实。

当前 codec V1 的 `payload_sha256` 是 canonical JSON 的完整 `CanonicalRequestFact` payload 哈希。JSON 解码器允许未知 additive 字段，但旧二进制重新 marshal 新字段后会计算不同的 payload hash，因此“旧 reader 可解 additive JSON”并不等于“旧 reader 可验证新 payload”。

## Decision

1. **本 Track 不在协议 handler 中做半成品生产接线。** 保留 `InjectGatewayMetadata` / `ProjectRequestMetadata` 作为 builder 的纯函数边界；生产接线必须等待统一 terminal fact builder，在同一个终态提交点一次性收集 request、upstream、response、timeline、usage、metadata 和 warnings。当前状态明确标记为“契约就绪 / 生产待接线”，避免伪造完整事实或只覆盖单协议。
2. **Codec V1 继续采用全量 payload hash，并保持 fail-closed。** 不修改 V1 的哈希语义，不把 metadata 从哈希覆盖范围中排除；否则同一版本会出现跨实现的完整性语义分裂。
3. **未来 additive payload 变更使用新的 codec version。** 当需要让旧 reader 稳定读取新 payload 时，新增 `CodecVersionV2`，定义明确的 hash projection/兼容策略，并提供旧 V1 只读兼容窗口或离线迁移；不得仅添加 JSON 字段而继续声称 V1 hash 向后兼容。`EnvelopeVersion`、`PayloadVersion`、`ProjectionEventVersion` 继续独立演进。
4. **本次只记录决策，不改变 wire 常量或历史数据。** 在没有 V2 reader/writer 和 fixture 前，修改版本常量会无谓地拒绝现有归档数据。

## Consequences

- 当前 requestfact metadata 不会被误认为已进入生产数据流；生产 builder 接线仍是后续任务。
- V1 对字段新增保持严格完整性语义，旧 reader 可能因 hash mismatch 拒绝新 payload，这是已知且显式的兼容边界，而不是静默接受篡改或丢字段。
- 后续实现需要补充：统一 terminal builder、至少一个真实 handler 端到端 fixture、V2 hash projection、V1/V2 golden corpus 及迁移/回滚说明。

## Verification

现阶段由纯 projection、wire round-trip、完整 payload hash 和旧版本拒绝测试覆盖；Track G 额外验证了 request log rate-limited not-run decision row 以及 autoroute outcome Prometheus 导出。
