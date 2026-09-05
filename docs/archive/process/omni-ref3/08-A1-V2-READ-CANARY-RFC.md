# 08 — A1：V2 会话读路径放开（决策 + 执行记录）

> 状态：**已执行（2026-08-07）**。原为灰度 RFC；用户决策"压缩+摘要一起切、默认开、可关、不灰度"（用户量小），故按平台级 kill-switch 直接全量放开。下文保留原灰度方案作为回滚/未来参考，§0 记录最终决策与执行结果。
>
> 关联：`omni-ref3/03-MULTITURN-ASSEMBLY.md` A1；前置 A2/A6/M7 均已落地。

## 0. 决策与执行结果（2026-08-07）

**决策**（用户）：压缩读 + 摘要读**一起切**到 V2；**默认开启**；保留**平台级 kill-switch**（可一键关回 V1，热加载，无需 redeploy）；**不灰度**（用户量小，灰度观测价值低于开销）。

**已执行的代码改动**：
| 文件 | 改动 | 测试 |
|---|---|---|
| `domains/hooks/compression/session_compressor.go` | `shouldUseV2` 删除硬编码 `return false`，改为 `settings.GetPlatformBool("sessions_v2_compression_read", true)`；保留 nil-component 守卫 | `should_use_v2_test.go`（5 例：默认开/kill-switch false/显式 true/nil 组件/nil receiver） |
| `cmd/gateway/main_pipeline.go` | 构造 `summaryService` 后，按**同一 flag** 调 `SetMessageSource(NewV2SessionBodiesSource(pool))`（压缩+摘要同开同关） | `cmd/gateway` 全量测试通过 |
| `settings/spec_sessions_v2_compression.go` | spec 从 `ScopeTenant/Default false` 改为 `ScopePlatform/Default true`，函数改名 `SessionsV2CompressionPlatformSpecs`，加 `EnvName` 与 `DangerLevel: Warning` | `settings` 全量测试通过 |
| `settings/specs.go` | 注册从 `TenantSpecs()` 移到 `PlatformSpecs()` | — |

**关键修正（执行中发现）**：原 spec 是 `ScopeTenant`，而 `GetPlatformBool` 只读 `ScopePlatform`——两者作用域不匹配，导致 kill-switch 形同虚设（永远走 fallback）。改为 `ScopePlatform` 后，admin 在 platform 设置 `false` 才真正生效。执行中 fail-open 仍在（`tryLoadV2State` 任一步出错回退 V1）。

**验证**：gofmt / go vet / `go build ./...` 全净；settings / hooks/compression / sessionsummary / session/v2 / ir / cmd/gateway 全套测试通过。

**回滚**：platform 设置 `sessions_v2_compression_read=false`（热加载，秒级）→ 压缩与摘要同回 V1。无需 redeploy。

---

## 1.（原 RFC，作为参考保留）目标与非目标

## 1. 目标与非目标

**目标**：把会话**读路径**从 V1（`request_logs` / LCS delta）切到 V2（`gateway.session_bodies` / 增量 delta），按租户灰度、可观测、可回滚，最终为 P3 退役 `request_logs_bodies` 铺路。

**非目标**：
- 本 RFC **不动写路径**（shadow write 已由 `sessions_v2.enabled` + `sessions_v2.shadow_write` 控制，独立于此）。
- 本 RFC **不退役 V1**（P3 才做，且需 V2 读全量稳定 ≥1 计费周期）。
- 不改客户端契约（submit-mode 探测、多轮拼接对外行为不变）。

## 2. 现状（`SOURCE-VERIFIED`）

| 切换点 | 当前 | 证据 |
|---|---|---|
| 压缩读 V2 | **关闭**：`shouldUseV2` 第 691 行硬编码 `return false`，flag 检查被注释 | `domains/hooks/compression/session_compressor.go:675-691` |
| 压缩 V2 读实现 | 已就绪 + **fail-open**：`tryLoadV2State` 任一步出错 → `ok=false` → 回退 V1 | `session_compressor.go:695-733`（`CacheV2.Get` / `Builder.BuildFromLatestOutbound` 出错即 warn + 回退） |
| 摘要读 V2 | 接口 + V2 实现已就绪，**未接线** | `domains/sessionsummary/{summarizer.go:messageSource, message_source_v2.go:v2SessionBodiesSource}`；`SetMessageSource` 无调用方 |
| Flag 基建 | 已成熟：`settings.GetTenantBool` / `GetPlatformBool`，热加载；`sessions_v2.enabled`/`shadow_write` 即此模式 | `cmd/gateway/main_v2_pipeline.go:549-550`（`GetPlatformBool`），`session_v2_init.go:33-34`（热加载说明） |
| 一致性工具 | 半就绪：`cmd/compression-bench` 能从 `request_logs` 回放、跑压缩、落临时表；**未做 V1/V2 双算 hash 对比** | `cmd/compression-bench/main.go:181,271`（`loadRequestLogs` + `processRow`） |

**关键结论**：A1 的代码改动量极小（§6 三处），风险几乎全在"V2 读出的历史与 V1 是否一致"。因此本 RFC 的重心是 **§4 一致性对比方案**，不是写新功能。

## 3. 切换点与改动清单（评审通过后才动手）

### 3.1 压缩读（`shouldUseV2`）
```go
// domains/hooks/compression/session_compressor.go
func (sc *SessionCompressor) shouldUseV2(tenantID string) bool {
	if sc == nil { return false }
	if sc.deps.CacheV2 == nil || sc.deps.Builder == nil { return false }
	return settings.GetTenantBool(tenantID, "sessions_v2_compression_read", false)
}
```
- 删第 681 行的 `|| false`、第 686-687 行注释、第 691 行 `return false`。
- **默认 false**（所有租户不变）；仅显式开了 flag 的租户走 V2。
- 已有 fail-open 兜底（§2），无需新增容错。

### 3.2 摘要读（`SetMessageSource` 接线）
在 `cmd/gateway/main_pipeline.go` 构造 `summaryService` 之后（约 `:1266-1268`），按**平台级 flag** 切源：
```go
summaryService := sessionsummary.NewSummarizer(pool, redisClient, ...)
summaryService.SetModel(cfg.ModelFor(...))
if settings.GetPlatformBool("sessions_v2_compression_read", false) {
	summaryService.SetMessageSource(sessionsummary.NewV2SessionBodiesSource(pool))
}
```
> ⚠️ 摘要是单例（`deps.SessionSummarizer`，`SOURCE-VERIFIED`：`main_pipeline.go:1245/1283` 仅赋值一次、跨请求复用），而压缩是 per-request 读 tenant flag。两者粒度不同——见 §7 R3。**已决策（§0，2026-08-07）**：按方案 A 解决，压缩与摘要统一用同一**平台级** flag（`GetPlatformBool`）一起切，不区分租户。

### 3.3 无第三处
写路径（shadow write）与本 RFC 解耦，不动。

## 4. 一致性对比方案（核心）

**问题**：V1（LCS delta-append）与 V2（增量 delta 重建）在边缘（attachment-only、orphan tool_result、submit-mode 误判）算法不同。直接翻 flag 会让"读出的 outbound body"在某些会话上与 V1 不一致 → 上送内容变化 → provider 行为/缓存命中变化。

### 4.1 离线双算（评审通过后、灰度前必做）
扩展 `cmd/compression-bench`，新增 `--compare-v2` 模式：对每个 `request_logs` 样本，
1. 跑 V1 路径得 `outboundV1`（现状）。
2. 跑 V2 路径（`OutboundBuilder.BuildFromLatestOutbound` / `BuildFromDeltas`）得 `outboundV2`。
3. 计算 `hash(outboundV1)` vs `hash(outboundV2)`，按一致/不一致分桶统计 + 落不一致样本供人工审计。

**验收阈值**（修订自 06 的"±5%"，因边缘本就算法不同，过严）：
- 正常会话（非 attachment-only、无 orphan） outbound hash **一致率 ≥ 99%**。
- 不一致样本 100% 人工审计，且每条都能归因到"已知的 submit-mode/边缘差异"而非"V2 bug"。
- 若发现 V2 bug → 不进灰度，先修。

### 4.2 在线 shadow-read（灰度期，可选但强烈建议）
对开了 flag 的租户，在 `tryLoadV2State` 成功后，**额外**异步算一次 V1 outbound 并比对 hash，记录指标 `v2_read_consistency{result=match|mismatch}`，不阻断请求。这样灰度期能看到真实流量的一致性，而非只靠离线样本。

> 这是 `NEW-DESIGN`，需在 §6 增一小段 shadow-compare 代码（仅灰度期，flag 控制开销）。

## 5. 灰度序列

| 阶段 | 范围 | flag 值 | 持续 | 进阶条件 |
|---|---|---|---|---|
| S0 离线双算 | 全量历史样本（离线） | n/a | 1–2 天 | §4.1 阈值达标 |
| S1 单租户灰度 | 1 个低风险租户（如内部/测试） | 该租户 = true | 3–7 天 | §4.2 mismatch 率 < 1%；错误率/延迟无回归 |
| S2 扩量 | 2–3 个租户 | 同上 | 1 周 | 同 S1 |
| S3 全量 | 所有租户 | 平台级 true | — | 监控持续绿 ≥1 周 |
| P3 退役 | 停写 `request_logs_bodies` | — | — | S3 稳定 ≥1 计费周期，单独 RFC |

**每阶段回滚**：把对应租户的 `sessions_v2_compression_read` 设回 false（热加载，秒级生效）。无需 redeploy。

## 6. 代码改动汇总（评审通过后执行）

| 文件 | 改动 | 状态 | 风险 |
|---|---|---|---|
| `domains/hooks/compression/session_compressor.go` | `shouldUseV2` 改为读 flag（§3.1） | **已执行**（见 §0，改为 `GetPlatformBool` 默认 true） | 低（fail-open 已在） |
| `domains/sessionsummary/message_source_v2.go` | 导出 `NewV2SessionBodiesSource(pool)` 构造器（供跨包构造） | **已就绪** | 无 |
| `cmd/gateway/main_pipeline.go` | `summaryService.SetMessageSource(NewV2SessionBodiesSource(pool))` 按 flag 接线（§3.2） | **已执行**（见 §0，R3 采用方案 A：平台级 flag 与压缩同开同关） | 低 |
| `cmd/compression-bench/main.go` | 加 `--compare-v2` 双算模式（§4.1） | 未执行（不灰度决策后无需求，保留为未来一致性核查工具） | 无（离线工具） |
| （灰度期）`session_compressor.go` | shadow-compare 异步打点（§4.2） | 未执行（不灰度决策后不需要） | — |

**无 DB 迁移**（schema 430 已存在）；**无新表**。V2-source 构造器已导出并经测试，§3.2 接线是唯一跨包改动。

## 7. 风险与待决

| 编号 | 风险 | 处置 |
|---|---|---|
| R1 | V2 读出历史与 V1 不一致 → 上送内容变化 | §4 双算 + 阈值；fail-open 已在 |
| R2 | `CacheV2`/`Builder` 在生产从未被真正读过（dead code 首次激活）→ 隐藏 bug | S0 离线双算即首次大规模激活，会暴露；fail-open 兜底 |
| **R3** | **摘要器是单例，flag 是 per-tenant** → §3.2 的接线粒度不匹配（要么全租户切摘要源，要么改架构） | **已决策并执行（§0，2026-08-07）**：方案 A —— 摘要按平台级 flag 切，与压缩读同开同关（全有或全无）。选择理由：用户量小，per-tenant 精细灰度（方案 B/C）的额外复杂度不值当。 |
| R4 | V2 读增加一次 DB 查询（join session_bodies+session_turns）→ 延迟 | L1/L2 cache 命中时短路；仅 L3 冷启动多一查；S1 监控延迟 |
| R5 | flag 误开（如平台级误设 true）→ 全量突变 | flag 默认 false + 按租户灰度；shadow-compare 报警 |

## 8. 评审清单（已决议，历史存档）

> 以下清单为决策前的原始待办；§0 记录了实际决议结果，均已按用户决策执行，不再是待办项。

- [x] §3.2 摘要源切换的粒度问题（R3）选哪个方案？→ **决议：方案 A**（摘要与压缩用同一平台级 flag 一起切），未采纳"建议方案 C"（仅切压缩读、摘要留 A1.5）。
- [x] §4.1 的 99% 一致率阈值是否可接受？→ **决议：不灰度**，跳过离线双算门槛，直接平台级全量放开（用户量小，灰度观测价值低于开销）。
- [x] §4.2 在线 shadow-compare 是否做？→ **决议：不做**（不灰度决策后失去意义）；fail-open 兜底保留。
- [x] §5 灰度租户选哪些？→ **决议：不灰度**，此项作废。
- [x] §6 改动是否认可？→ **认可并已执行**，见 §0 表格。

## 9. 决议后路径（已完成）

- ~~评审通过 → 执行 §6（按 §5 灰度）。~~ 实际决策跳过灰度，直接全量执行 §6（§0 已记录）。
- ~~若选 R3 方案 C → 本 RFC 的 §3.2 删除...~~ 实际选择方案 A，§3.2 接线按平台级 flag 保留并执行，摘要未拆分出 A1.5。
- 下一步：A1 全量稳定运行满 1 个计费周期后 → 进 P3（退役 `request_logs_bodies`），另起独立 RFC。
