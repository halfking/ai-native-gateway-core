# 24小时修改审计报告（2026-07-27 至 2026-07-28）

**审计时间:** 2026-07-28
**审计基线:** `8da7db526451f4c08cc18a04883a9fb66f7abd23`（24小时前的共同基线）
**审计范围:** `git rev-list BASE..HEAD` 共 97 个提交（87 个非合并、10 个合并），265 个文件，`+16029/-4130`  以及本次审计整改未提交差异
**审计方法:** Standards / Spec 双轴复核、核心模块测试、静态检查、迁移编号与差异门禁检查
**审计结果:** ⚠️ **NO-GO**：代码整改已完成并通过受影响模块测试，但仍有发布阻断项未验证或未闭环，不得据此宣称全量切换完成。

## 修改总结

- 请求链路：arrival WAL、终态保护、请求体/遥测字段、流式断开和 keepalive 生命周期。
- 路由状态：URSM v2 Redis/LRU、credential state 并发更新、探测/恢复、quota 路由门禁、sticky/intent 双写。
- 协议转换：provider-specific tools、streaming tool args 校验、Anthropic 错误信封和 4xx body 处理。
- 管理面与部署：client-perception view、迁移 458-461、健康等待超时、版本/发布文档。
- 统计以 `8da7db52..HEAD` 为准；此前报告中的 89 个提交、84 个文件和 `+2464/-646` 不符合仓库事实，已纠正。

## 本次整改

- 修复 URSM `Ready=false` 时 LRU 全命中绕过 recovery gate 的问题；公开 `FilterAndScore` 恢复真实 Ready 检查，快照路由使用 `FilterAndScoreReady`。
- 增加 tenant-aware URSM node/window key、NodeMirror 查询和请求结果写入；空 tenant 保留旧 key 兼容入口。
- 修复 routing 与 streaming executor sticky Redis store 的无锁读取/删除，统一使用锁保护的快照。
- 修复两条 Anthropic→OpenAI 流式桥接路径：无法修复的 tool 参数不再透传非法 JSON，而是发送协议错误并终止该流。
- 删除重复的 `461_request_wal_hot_request_id_unique.sql`，保留唯一索引版本，消除同编号 forward migration 的执行歧义。
- 清理新增文档尾随空格，恢复 `git diff --check` 门禁。

## 双轴审查结论

### Standards

- 通过：受影响 Go 代码已 `gofmt`，`git diff --check` 通过，核心迁移编号检查不再包含新增重复 461。
- 通过：受影响模块编译与测试通过；sticky 共享字段访问已改为锁内配置、锁外 I/O。
- 仍需关注：仓库历史迁移存在既有重复编号，不能用简单全仓重复编号扫描替代部署账本校验。

### Spec

已闭环：L-1/L-2 WAL 与日志终态路径、D-2 范围内 body 读取、G-ID-1/2、F-1/F-2/F-3、D-1、Ready gate、tenant-aware URSM 读写、F-5 非法 JSON 阻断。

未闭环或未完成发布验收：

- **S-3:** `routing_state_source` 尚未传播到 request context、attempt、request log、decision log 和 metrics；当前不能统计 NodeMirror hit/miss/stale/fallback。
- **URSM authoritative 纯度:** 未就绪时仍存在 legacy state fallback；需按发布方案验证 authoritative 模式下旧状态源零 live 调用。
- **Session V2 单 owner:** pipeline `SessionPersistHook` 与 telemetry mirror owner/时序仍需真实 upstream 前后链路验收。
- **迁移与真实依赖:** PostgreSQL 上 459 down/up、460/461 up/down、唯一索引对既有重复数据的清理尚未在真实数据库执行；Redis/PG 故障注入、E2E、部署主机验证未执行。
- **IR 默认:** 设计目标要求 IR 默认路径，但当前 live 默认仍由 feature flag 保持 Legacy；不能把目标态描述为已切换。
- **运行审计:** provider profile 当日聚合、告警并发去重和告警持久化失败处理仍需独立整改/验收。

## 验证结果

- ✅ `gofmt`（本次修改文件）。
- ✅ `git diff --check`。
- ✅ `go test ./...`：全仓通过（211 个包结果，无失败）。
- ⚠️ 受影响模块 `go test -race`：URSM v2、routing、streaming/executors、Anthropic transform 通过；`domains/streaming/TestRedisFormatCache_TTL` 的既有 TTL 过期断言失败（不在本次整改文件范围）。
- ⚠️ 未执行真实 PostgreSQL/Redis 迁移回滚、故障注入、部署主机和完整协议 E2E。
- ⚠️ 严格 secrets 扫描受仓库既有 `.env*`/示例文件命中影响；未发现本次整改新增秘密。

## 结论与后续

当前状态是“代码修复完成、核心回归通过、发布验收未完成”。在 S-3、authoritative 纯度、Session V2 owner、真实迁移/依赖/E2E 验证完成前，不应 merge/push 或宣称 GO。若业务明确允许带风险提交，需单独记录延期项、责任人和发布阻断豁免；本审计不自动豁免。

**审计人:** ZCode
**审计日期:** 2026-07-28
