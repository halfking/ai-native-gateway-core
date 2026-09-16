# D03 — 三层缓存 provenance（original → sanitized → compressed）

> 领域编号: D03 ｜ 最近更新: 2026-09-17 (初版) ｜ 状态: v1

## 1. 领域边界

**管**：会话内容三层缓存（original/sanitized/compressed）的完整 identity/occurrence 映射；SanitizedMessageRefs 与 AlignmentMap 生产链路接入 V2 metadata；sanitizer 跨进程 offset 原子预占；provider-window source telemetry；四类易混对象 TTL。
**不管**：压缩策略选择与触发时机（D05）；队列缓存（D04）；分区存储（D07）。

## 2. 参考基线

设计文档：
- `docs/03-design/02-feature-design/会话优化v4/03-缓存映射与请求队列设计.md` — 三层映射 + TTL（现行主文档）
- `docs/03-design/02-feature-design/design/QUICK-REF-THREE-TIER-CACHE.md` — 快速参考
- `docs/session-v2-config-reference.md`、`docs/implementation/session-v2-followup-design-20260829.md` — V2 metadata 契约
- 历史 spec：`docs/archive/process/specs/2026-08/2026-08-09-session-compression-three-tier-cache.md` 等（追溯用）

代码入口：
- `domains/hooks/compression/` — alignment.go（AlignmentMap）、session_cache.go、session_compressor.go、quality_score.go、e2e_alignment_test.go
- `domains/secretmask/`、`domains/hooks/security/` — 脱敏（sanitized 层生产者）

## 3. 检查清单

1. **三层 provenance 完整**：任一 sanitized/compressed 内容都能沿 identity（哪条原始消息）+ occurrence（该消息内哪次出现）回溯到 original；映射缺口有检测（测试或运行时校验）而非静默。
2. **offset 原子预占**：sanitizer 跨进程并发时 offset 预占是原子的（锁/事务/预登记表），不存在两进程对同一区间重复编号；窗口内改动的 sanitizer 路径不引入新的竞态窗口。
3. **V2 metadata 接入**：SanitizedMessageRefs 与 AlignmentMap 的生产链路产物确实写入 V2 metadata（而非只留在内存对象）；读侧（摘要/取证/前端）能消费。
4. **provider-window source telemetry**：窗口遥测能区分内容来源层（original/sanitized/compressed 哪层发出的请求），指标不串层。
5. **四类易混对象 TTL**：按主文档 §TTL 表核对各缓存对象过期策略，新增缓存对象必须归类到四类之一。
6. **压缩质量**：quality_score 参与准入/回退，压缩后 provenance 不因重建而断链。
7. **并发**：session_cache 并发读写测试（session_cache_concurrency_test.go 基线）覆盖窗口内新增路径。

## 4. 历史回归点（轮末回注区）

- [初版] e2e_alignment_test / session_cache_concurrency_test 为健康面基准；三层链路的任何重构必须保持两测试绿

## 5. 子代理派发提示词

```text
你是 D03（三层缓存 provenance）只读审计子代理。工作目录：本仓库根。
第一步：Read docs/audit/playbook/conventions.md 和 docs/audit/playbook/domains/D03-three-tier-cache.md 全文。
第二步：按域文档 §3 检查清单逐条核对，审计窗口：<窗口>；改动文件清单：<该域相关子集>。
重点：domains/hooks/compression/ 与 domains/secretmask/ 的窗口内 diff，及 V2 metadata 写读两侧。
只读不改。输出按 conventions.md §4 结构，每条发现带 file:line 与触发路径。
```
