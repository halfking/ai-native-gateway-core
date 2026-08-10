# OmniRoute Final Cache Commit

## 做了什么

审计当前会话压缩、V1/V2 缓存和 sanitize 链路，并研究 OmniRoute 的
post-guard authoritative body、inflation guard、fidelity gate、hard budget、
tool-call pairing 和 memo isolation 机制。本次选取最小且高价值的一项：缓存
基线必须等于进入 executor 的最终客户端协议请求体。

## 改动清单

| 文件 | 改动 |
|---|---|
| `domains/hooks/compression/session_compressor.go` | 新增 `CommitFinal`，完整继承 SessionState，重算最终 body 元数据 |
| `domains/streaming/handler.go` | cache injection 后、executor 前提交最终缓存 |
| `domains/hooks/compression/final_commit_test.go` | mock supplier、一致性、V2 隔离、非法 session、状态继承测试 |
| `security/sanitize/compression_cache_integration_test.go` | 真实 sanitize middleware + mock supplier 组合测试 |
| `docs/design/2026-08-10-omniroute-final-cache-commit.md` | 设计、边界、测试 seam、回滚 |

## 为什么这样做

旧流程在 `Prepare` 内写缓存，之后 handler 仍可能执行 NeverWorse 回退、恢复
tools、重排 prefix 或注入 cache-control。下一轮 delta 可能基于一个供应商从未
收到的 body。新流程保留 `Prepare` 兼容写入，再由生产 handler 用最终 body
覆盖提交，兼容 replay/preview 调用方且可单 commit 回滚。

## 验证结果

- `go build ./...`: PASS
- `go vet ./...`: PASS
- `go test ./domains/hooks/... ./domains/session/... ./domains/streaming/... ./security/sanitize/... -count=1`: PASS
- `go test -race ./domains/hooks/compression ./security/sanitize -run 'TestCommitFinal|TestSanitizeCompressionCache' -count=1`: PASS
- Mock supplier: 缓存 body 与供应商 request body 字节一致
- Sanitize: 原始手机号不进入缓存或供应商，占位符存在，`AuditedAt > 0`

## 遗留与风险

- `/v1/messages` 与 `/v1/responses` 尚未统一进入 Chat sanitize/compress 主链。
- 非流式 response restore 时序、sanitize session 权威 ID 和 Redis offset 原子分配
  仍需独立 P0/P1 任务。
- V2 read/write 开关一致性、L1 更新后的字节预算驱逐、AlignmentMap pre-strip
  完整性尚未在本次修改。
- OmniRoute fidelity gate、hard budget 与 Anthropic context editing 暂未迁移。

## 下一步建议

1. 独立设计四端点统一 pre/post-dispatch pipeline。
2. 先修 sanitize 权威 session 与占位符原子分配，再修非流式 restore。
3. 缓存基线稳定后迁移 fidelity gate 和 hard-budget post-pass。
