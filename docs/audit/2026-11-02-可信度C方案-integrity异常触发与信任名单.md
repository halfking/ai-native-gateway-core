# 2026-11-02 可信度 C 方案：integrity 异常触发 + 信任名单

## 1. 目标

承接 B（探活成功 → 独立 ScanNode）之后，按建议落地：

1. **C**：真实会话 / 指纹漂移检出 `model_mismatch` / `fingerprint_drift`（high/critical）→ 节流触发独立可信度 ScanNode
2. **信任跳过名单** `model_credibility.trusted_skip`
3. **钉死**：非 featured 不自动扫；手动 `ScanNode` 不受 featured / trusted 限制

## 2. 设计要点

| 面 | 做法 |
|---|---|
| 活信号 | `integrity.NotifyRecorder` 包住 PoolRecorder，**采样前**回调（避免 10% sampleRatio 掏空触发面） |
| 漂移 | `IntegrityFingerprintDrift.SetCredibilityOnDrift`，commit 成功后回调 |
| 与 planner | `IntegrityProbePlanner` 仍只入队 `integrity_verify`（探活核实）；本路径发标定提示词 —— **正交** |
| 节流 | 与 B 共享 inflight；冷却独立默认 1h，**不受 verified 长冷却压制** |
| featured | scope=featured 时非特色跳过（与定时 / B 同口径） |
| trusted | 定时 ScanAll + B + C 全跳过；手动 trigger 不跳 |
| 开关 | `trigger_on_integrity_anomaly` 默认 true；与 `enabled`（定时）独立 |

## 3. 设置项

- `model_credibility.trigger_on_integrity_anomaly` (bool, default true)
- `model_credibility.integrity_anomaly_cooldown_hours` (int, default 1)
- `model_credibility.trusted_skip` (string JSON array, default `[]`)
  - 条目：`model` / `provider/model` / `provider:model`

## 4. 读数

- `go test ./domains/streaming/integrity/ ./bg/ -run '…'` ✅
- `go build ./cmd/gateway/` ✅

## 5. 未做

- 未部署 211/245/154（需老板明示）
- 未改 IntegrityProbePlanner 语义
- 管理面 UI 编辑 trusted_skip 仍走通用 settings 表单
