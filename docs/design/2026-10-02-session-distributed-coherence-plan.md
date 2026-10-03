# 会话分布式一致性下一轮修复方案

2026-10-02。依据本轮真实本地 Redis 7.4.11 / PostgreSQL 17 合成观测，见 [审计报告](../audit/2026-10-02-conversation-storage-stream-audit.md)。本文件是下一轮方案，下面三项没有在本轮实现；不能把观测测试的 PASS 当作一致性验收。

## 可重复失败

运行 `bash scripts/audit/run-session-storage-stream.sh --require-coherence`。当前预期退出非零：

1. A 更新共享 Redis generation 后，B 的已有 L1 仍返回旧 generation；新的 C 冷读得到新 generation。
2. A、B 同时读改写同一会话，两次 `StripsApplied++` 最终 Redis 只有 1。实例内 striped mutex 不提供跨进程原子性。
3. Redis 元数据记录 1 条旧消息及旧 hash，V2 PG latest outbound 已有 2 条新消息；冷读把两者拼接。`turnReader != nil` 无条件绕过正文 hash 验证，无法证明两者来自同一版本。

这些证据来自缓存 API 和真实 TurnReader，不是最终 provider 派发的泄漏证据。下一轮必须增加完整 handler 负对照，确认保护层的失效行为和历史保留是否符合契约。

## 修复顺序与契约

| 顺序 | 修复 | 验收标准 |
|---|---|---|
| 1 | 给会话持久化版本与请求提交顺序建立明确契约；正文快照和元数据必须携带同一不可复用版本/消息摘要。V2 snapshot 指向具体 turn，禁止独立读取任意 latest 后拼接。 | PG 滞后、Redis 滞后、后到的旧请求提交、重试同一请求均不能生成混合状态；不同 envelope 的同一消息数组也能合法验证。 |
| 2 | 对共享写入增加 fencing/CAS，处理 Update 冲突并定义有界重试；所有 Set/CommitFinal/recovery 写入口遵循相同版本规则。 | 两个独立进程并发写同一 session，无覆盖较新版本；计数合并契约可验证；丢租约工作者不得提交。 |
| 3 | L1 命中验证共享版本，或定义可证明的会话路由所有权；失效广播只能优化延迟，不能作为唯一安全判据。 | A 写后 B warm read 与 C cold read 一致；断线/订阅丢消息/Redis eviction 仍保守失效。 |
| 4 | 将完整真实 Redis/PG 合成夹具扩展到 V2 writer→reader→compressor→handler，固定派发 capture。 | 冷进程恢复保留完整已脱敏历史；不把版本未知的旧裁剪标记绑定新字典；失配不得把缓存历史静默当作新会话丢弃。 |

保守回退必须保留能够验证的完整已脱敏历史；不能通过丢缓存正文、关闭输出 gate 或使用遮蔽后的敏感工具参数来“修复”验收。旧无版本数据需要明确迁移/失效策略，不能自动补写一个看似匹配的新 generation。

## SSE 延迟与容量

本轮维持终态/EOF 检查语义，修复终态帧绕过 1 MiB wire 容量检查。下一轮需要：

- 对完整生产写入链测量 live heap、RSS、GC、并发 1/8/32/128 请求和长时间取消/断连；本轮 B/op 只是累计分配，不是同时存活内存。
- 同时预算帧数、已解析结构、工具 JSON、还原后扩张和终态输出副本。固定 heartbeat 不计入模型输出缓冲，但 transport pending frame 另有 16 MiB 上限。
- 若要降低首字延迟，先定义有界匹配规则或可证明安全的流式解析器，并用跨片段任意空白/Unicode 转义/完整工具 JSON 的负对照证明可提前释放范围。当前任意长度正则不能由固定 lookbehind 安全解决。

没有生产、多实例网关集群、真实供应商或付费请求验收；不执行部署或凭据改动。
