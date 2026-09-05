# 2026-08-18 — URSM Redis delimiter-safe key 兼容迁移设计冻结审计

## 需求与范围

冻结 `NodeKeyForTenant`、`WindowKeyForTenant`、`CandidateIndexKey` 的 delimiter-safe Redis key 兼容迁移设计，定义唯一 Migration owner、旧 key dual-read、新 key cutover、TTL/清理和回滚边界；提交并交接下一会话规划。本次仅修改 Markdown 文档，不修改生产 Go、Lua、SQL、Redis、PostgreSQL、环境配置或部署状态。

## 基线与所有权

- 审计时当前代码基线：`624845b2f928febfb974c4b2a6313626d7b7dc7a`，分支 `main`。
- T0 修正收口基线仍为 `df6c6546310ec07ca5abb636cf5d8759bf2c3502`；其起始审计基线与修正证据见 [T0 审计记录](./2026-08-18-session-v4-t0-audit.md)。
- 本轮 owner：Coordinator / 文档冻结。Migration owner 尚未具名、未取得 ledger ID、未获得 URSM key-schema 文件移交；因此不具备实施或切换权限。
- 改动文件：会话优化 v4 导航、T0 ownership、key migration 决策和本审计记录。

## 审计结论

**`M5-0/T0 = BLOCKED / NO-GO`。** 本轮冻结设计，未关闭 delimiter-safe key blocker，未开始 G1/G2/G3/G4/G5，未进行真实依赖验证或发布。

当前 legacy `:` 拼接 grammar 对 delimiter-bearing tenant/model/candidate component 不能无碰撞编码，也不能从历史歧义 key 可靠恢复 identity。修复禁止就地改变 legacy bytes 或根据字符串推断 tuple。

设计冻结结论：

1. 使用显式版本化 `k2` canonical namespace；variable components 采用 `base64.RawURLEncoding`，legacy namespace 原样保留。
2. key schema mode 独立于 `URSM_V2_MODE`，固定为 `legacy`、`dual`、`canonical`；任何变更先关闭 ready gate。
3. preflight 以只读 scan 和带 checksum 的 ledger 将旧 key 分类为 migratable、canonical-present、ambiguous、excluded-non-authoritative。ambiguous/conflict 默认 `NO-GO`。
4. dual mode 的 canonical-first read、legacy fallback、node/window/dedup key set、一致性双写、Lua/CAS、PTTL preservation、resumable work item、coverage 与 cleanup 都必须在实现前按决策验证。
5. Migration owner 对 `domains/ursm/v2/**` 的 key-schema 文件只可取得协调方从 URSM owner 显式移交、并在 ledger 列名的精确文件，确保单文件单 owner。
6. 正确的发布门禁映射是 `M5-0/T0 -> M5-2/G1 -> M5-3/G2 -> M5-4/G3 -> M5-1/G3 -> M5-5/G4 -> M5-6/G5`；T8/245 必须在 G5 GO 后。

## 文档产物

- [14-URSM Redis delimiter-safe key兼容迁移冻结决策](../../../03-design/02-feature-design/会话优化v4/14-URSM Redis delimiter-safe key兼容迁移冻结决策.md)：canonical grammar、mode/metadata、preflight/ledger、dual read/write、TTL/cleanup/rollback、测试矩阵与 gates。
- [13-T0契约冻结与所有权](../../../03-design/02-feature-design/会话优化v4/13-T0契约冻结与所有权.md)：当前基线、M5/G 映射、owner handoff 规则。
- [会话优化 v4 导航](../../../03-design/02-feature-design/会话优化v4/00-README.md)：新增 13/14 号入口，修复 runbook 与稳定性审计链接。
- [T0 审计记录](./2026-08-18-session-v4-t0-audit.md)：明确区分审计起始基线 `9268…` 与 T0 修正收口基线 `df6c…`。

## 审查与验证

已完成：

```text
- git diff --check
- Python Markdown 相对链接解析：本轮 5 个文档、17 个本地链接均可解析
- `./scripts/scan-secrets.sh --mode=strict --paths <5 changed docs>`：5 files scanned、0 findings、`CLEAN`
- 独立 Spec review：确认设计符合 approved plan，且未将 T0/G1/G4/release 表述为完成
- 独立 Standards review：发现并已修正 M5/G 映射、URSM/Migration owner 范围重叠、相对链接和基线叙事问题
- 独立 follow-up review：无剩余可操作问题
```

未执行且不适用本次纯文档冻结：Go unit/race/vet/build、pre-commit、Redis/PG/provider 集成、部署、真实 Redis restart/`NOSCRIPT`/PubSub、真实 PG lease/fencing、provider/slow-client/chaos。它们仍分别是实现后的 G1/G3/G4 证据，不能因本轮文档审计而视为通过。

## 风险、阻塞与交接

1. 现有 legacy Redis 状态可能含歧义 key；没有可信 operator/PG/config identity mapping 时，不得自动 copy 或放入 authoritative coverage。
2. Redis Cluster 下 node/windows/dedup 的 Lua atomicity 尚未验证，必须在 migration implementation 前冻结 hash-tag topology。
3. candidate index 没有 production routing caller；其 canonical rebuild 不能擅自启用为 routing authority。
4. 真实 Redis/PG/provider 证据不存在，因此 release-ready 结论仍为否。

下一会话应先由唯一 Migration owner 在 ledger 中具名，并以只读 preflight inventory 验证 legacy state 分类。随后才进入 L1 的 canonical key primitives TDD；在设计规定的模式、ledger、preflight 和测试都落地前，不得改写现有 key helper 的 legacy 输出或进入 G1。
