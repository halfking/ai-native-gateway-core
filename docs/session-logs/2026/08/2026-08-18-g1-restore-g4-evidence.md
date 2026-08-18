# 2026-08-18 会话日志：G1 门禁恢复 + G4 真实 Redis/PG 证据（ZCode 续接会话）

## 1. 任务概要

承接 `handoff-20260818-173135.md`：Go 1.26 vendor 修复 → G1 恢复 → G4 证据。全部完成，另产出 2 个 P1 新发现。

## 2. 结果

1. **Go 1.26 vendor 不兼容（ticket 转 RESOLVED）**：根因不是 toolchain/vendor（`crypto/rc4` 在本机 GOROOT 实际存在、vendor 完整、`go build ./...` 通过），而是**构建缓存中毒**（双 Go 安装共享 GOCACHE + 长驻 `go run` 与 `go clean -testcache` 循环并发）。修复=路径 D `go clean -cache`（零 repo 改动）。详见 ticket §9。
2. **G1 全仓门禁恢复**：`go clean -testcache && go test -count=1 ./...` 连续 **8/8 exit 0**（19:11–19:26，日志 /tmp/g1-gate-20260818.log）；新远端 HEAD（ef6c7fb1d 含 3 个新远端提交）复验 1 轮 exit 0。
3. **dispatch `TestPipelineWiresQueueMirror` flaky**：8/8 全过，确认缓存次生污染，非测试 race。建议关闭独立 flaky 案。
4. **G4 证据（隔离真实服务）**：详见 `docs/handoff/2026-08-18-g4-real-redis-pg-evidence.md`。
   - 通过：preflight/copy/cleanup 全矩阵、NOSCRIPT 回退、PTTL 实时扣减、幂等、generation 栅栏、cleanup 护栏、50/50 并发 advance CAS、陈旧 epoch CAS 拒绝、（补列后）真实 PG ledger 落库。
   - **F-1（P1）**：copy Resume 守卫（copy.go:169-178）不检查 classification，"重扫→幂等 copy→cleanup" 链条会把 canonical_present 条目盖成 StatusCopied，cleanup 随即 DEL canonical 键（ledger 状态演化铁证：classified→copied→cleaned）。100% 确定性复现。
   - **F-2（P1）**：pg_store.go INSERT 的 `rollback_deadline` 列在 080 DDL 中不存在，真实 PG `--apply` 必 42703；scratch 补列后全链路正常。
   - **M5-0/T0 维持 NO-GO**（新增 F-1/F-2 理由）。

## 3. 改动清单

- 仅 docs：ticket `2026-08-18-go1.26-vendor-incompat.md`（§9 解决记录）、本日志、`2026-08-18-g4-real-redis-pg-evidence.md`（新）。
- 未动任何生产代码/Lua/SQL/CLI/vendor/go.mod（遵守 14 §0 移交边界）。
- commit：`ef6c7fb1d`（ticket 解决记录，已 push）；本日志+证据文档随后提交。
- 临时资源：docker 容器 `k2-g4-redis`/`k2-g4-pg` 会话末清理；`/tmp/k2-g4/`、`/tmp/g1-run-*.out` 留存复验。

## 4. 遗留

1. F-1/F-2 修复（owner；回归用证据文档 §6 命令）。
2. 17 号 §11 变更记录追加（L3 owner 合并时）。
3. 两 CLI window 分类不一致 + entries.tuple_tenant 空值（次要，建议 owner 顺带）。
