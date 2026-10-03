# 会话脱敏：真实本地存储、多进程与 SSE 边界复审

2026-10-02（Asia/Shanghai）。交接基准 `a1a9ac17f160d58b8d2c83f2232285885435909c`；恢复时 main 已为 `20fbdba74ced51cb8288b9d59ab3f654c5fe69eb`，本轮隔离工作区以此为基线。原主工作区的用量趋势与菜单修改未复制、未覆盖。没有 pull、部署、凭据注入、生产资源修改或真实供应商请求。

## 结论

新增真实 Redis/Postgres 合成执行证据，修复两处可重复的边界缺口，并修复扫描中新出现的可达依赖漏洞。**分布式缓存一致性仍未通过**：三个可观测问题已形成下一轮修复方案，不能把常规测试 PASS、无 race 或输出 gate 接线当作生产验收。

| 发现 | 实际证据 | 本轮状态 |
|---|---|---|
| SSE 终态正文绕过 1 MiB 累计 wire cap | 超限 1 字节、pending + 终态超限、生产 writer 的两个小于 1 MiB 帧合计超限均在旧源码下未阻断 | 已修复：heartbeat 后、JSON 解析/终态释放前检查 pending + 当前完整帧；等于上限接受，超过阻断 |
| Redis 旧审计/审批字段复活 | 新 state 写入 false/0/空值，冷实例仍读到旧 `SensitiveDetected`；负对照也保留旧批准状态与 ID | 已修复：显式写入全部 v6 audit/approval 零值；真实 Redis + PG 冷读及默认回归通过 |
| 热实例 L1 不随另一实例写入失效 | B warm generation=old；C cold generation=new | 未修复，分布式一致性方案 #3 |
| 实例内 Update 互斥无法保护跨实例读改写 | 两次计数增加，最终 Redis 只得到 1 | 未修复，方案 #2；不是 Go data race |
| Redis 元数据与 V2 latest outbound 混合版本 | Redis 的旧 hash / MsgCount=1 与 PG 新正文 MsgCount=2 返回为同一 cache result | 未修复，方案 #1；本轮没有声称证明最终 provider 泄漏 |
| OpenTelemetry exporter 配置日志可泄漏 endpoint URL | `govulncheck` exit 3，GO-2026-6505，v1.44.0，修复版 v1.45.0 | 已升级关联 OTel 模块及必要传递依赖，重建 vendor；最终扫描可达 0 |

依赖修复依据扫描回溯与 vendored OTel v1.45.0 CHANGELOG 的 #8438。上游同时有 WithEndpointURL 路径语义变更；本仓库实际使用 WithEndpoint，未调用 WithEndpointURL。本轮未扩展为原始业务日志/Redis 可逆字典的明文治理。

## 真实存储与多进程证据

一次性本地 Docker 服务：镜像 `redis:7.4.11-alpine`、`postgres:17-alpine`，端口仅发布到 127.0.0.1，无自动拉取镜像；PG 使用临时 trust 配置，无真实凭据。PG 每个测试用 `internal/testdb.Create` 分配数据库并清理；Redis 仅使用唯一合成会话键，未使用 FLUSHDB/FLUSHALL。运行脚本 EXIT 清理自己创建的容器及匿名卷。

- 两个独立 OS 测试进程分别通过真实 input middleware 提交 16 个不同合成值：最终 32 个映射，offset high-water=32。共享 Go 锁无法解释该结果。
- race/宿主机争用下，租约忙会返回 503 且不派发；最初“所有请求必须 200”的测试退出 1，只有 17 项成功。修订后的测试明确验证每次 503 未派发，再做有界重试；最终 32 项成功，两个 worker 分别记录 1 / 3 次准入重试。这不能解释为没有拒绝请求或已完成吞吐验收。
- 真实 Redis lease loss 后旧工作者返回 503，不能覆盖新工作者提交的字典。
- 新缓存实例通过真实 Redis + 实际 V2 TurnReader/PG 查询恢复正文，旧裁剪、审计和本轮统计清零，TTL 约 30m。
- 两个真实 Redis governance 客户端验证零值替换、冷读和 WRONGTYPE 错误契约。

PG fixture 是最小查询形状的 `session_bodies_unified` 表，不是完整生产 schema/迁移链/RLS/分区验收；compression.RedisBackend 用真实 go-redis 实现接口，本轮没有声称加载完整 gateway 部署接线。`TestStorageAuditSanitizeWorker` 在父进程的 SKIP 是 subprocess helper，父测试确实启动两个独立子进程执行它；没有把缺外部服务的 SKIP 当作验收。

## SSE 延迟与内存边界

本地 HTTP 客户端→生产 `interceptingStreamWriter`→mandatory sensitive guard：合成模型先生成一条 delta，再等待 150ms 后 terminal/EOF。普通运行 heartbeat 约 0.4–0.9ms，模型首字约 151ms；race/并行负载下约 161–163ms。此结果证明文字等待终态，并非真实供应商 TTFT 或时延 SLA。

容量基准覆盖接近 1 MiB 的 wire 数据：128 字节小帧 8191 帧累计分配 56,555,712 B/op，8192 字节帧 127 帧为 19,341,328 B/op，单帧 1 MiB 为 7,371,080 B/op。见 [benchmark 原始输出](verify/session-storage-stream-2026-10-02/benchmark.txt)。B/op 是累计 Go 分配，不是 live heap 或 RSS；逐帧 map、文本 lane、regex、输出拼接产生放大，不能把 1 MiB wire cap 当作 1 MiB 请求内存上限。该单次基准与其他本地测试并行，时间只作示例，没有并发请求吞吐、长时间 steady state、p95/p99 或取消/断连泄漏验收。

新 cap 包含当前终态帧，正好填满 pending 的流再附加 `[DONE]` 会超过总预算；固定可信 heartbeat 豁免。EOF 在预算内可安全释放。transport parser 另有 16 MiB 单帧上限，且在进入 guard 前已暂存原始帧，因此此次修复也不能证明整个 SSE 写入链只有 1 MiB 内存。

## 可重跑命令与负对照

```bash
# 独立 disposable Redis/PG，观测保留问题；默认命令不把一致性作为成功条件
bash scripts/audit/run-session-storage-stream.sh --race

# 一致性要求开关：当前明确预期 exit 1，两个观测测试报错
bash scripts/audit/run-session-storage-stream.sh --require-coherence

go test -p=2 ./security/sanitize -run '^$' \
  -bench BenchmarkStorageAuditStreamCapacity -benchtime=1x -count=1
```

旧源码负对照：把基线 `20fbdba7` 的 `session_cache.go` 与 `stream_compliance.go` 通过 Go `-overlay` 临时替换到同一套新增测试，不修改工作区文件。`TestSessionStateV6Fields_ZeroValuesResetPriorAudit`、真实冷读、两条 terminal 超限及生产 writer 合计超限均失败，exit 1。见 [负对照](verify/session-storage-stream-2026-10-02/negative-control.txt)。

严格一致性开关下，测试报告 warm/cold generation 不同、两次 Update 仅保留 1、旧 hash 与新正文混合，exit 1。见 [一致性失败输出](verify/session-storage-stream-2026-10-02/coherence-red.txt)。默认观测模式 PASS 仅表示探针执行完毕，绝不是这些问题已经修复。

## 验证回执

核心源码与依赖变更完成后执行；最终结果在本节补记。初次 race 回归发现旧测试要求“省略审计零值”，与正确的 HSET replacement 契约冲突，已修正后完整重跑；该初次失败没有计为通过。

首轮全仓默认测试 exit 2，发生两项失败，见 [完整失败记录](verify/session-storage-stream-2026-10-02/default-initial-red.txt)：新增的 `SESSION_AUDIT_PG_URL` 未被公共 harness 注入，被仓库自动派生的环境变量覆盖门捕捉到。本轮改用已有 `TEST_PG_URL` 约定，未添加门禁豁免，也未修改公共 gate 测试；[单独复验](verify/session-storage-stream-2026-10-02/gate-env-fix.txt) 通过。既有 IR serialize p99 测试测得 14.16ms > 10ms，未改源码/阈值，单独重复 3 次分别约 0.31 / 0.61 / 0.29ms，通过，见 [记录](verify/session-storage-stream-2026-10-02/p99-isolated-rerun.txt)。这只说明失败未稳定复现，未证明 p99 回归的确定根因；最终全仓再次执行，不能用 targeted 重跑冒充全仓通过。

| 检查 | 结果 |
|---|---|
| 4 包真实合成集成 + race | exit 0；[原始记录](verify/session-storage-stream-2026-10-02/integration-race.txt)，无 Go race 报告；一致性观测仍为 false |
| 采用现有 TEST_PG_URL 后的真实合成集成 / macOS Bash 3 runner | exit 0；[约定切换后](verify/session-storage-stream-2026-10-02/integration-final.txt)、[Bash 3](verify/session-storage-stream-2026-10-02/bash3-runner.txt)；不放宽服务/隔离检查 |
| 8 包默认 race | exit 0；[原始记录](verify/session-storage-stream-2026-10-02/race.txt) |
| 全仓默认 make test | exit 0，312 包通过、57 包无测试；[全仓记录](verify/session-storage-stream-2026-10-02/default-final.txt)；不是 integration 标签/生产验收 |
| 全仓 go vet / gateway build | exit 0；没有把编译通过当作生产接线验收 |
| go mod verify | exit 0，all modules verified |
| govulncheck | exit 0，可达 0；1 个导入包、5 个 required module 漏洞当前未调用；[记录](verify/session-storage-stream-2026-10-02/govuln-final.txt) |
| 秘密扫描（10 个本轮源码/脚本文件） | exit 0，findings=0；未声称全仓无秘密；[记录](verify/session-storage-stream-2026-10-02/secrets.txt) |
| 报告/方案/执行证据扫描 | exit 0，15 文件 findings=0；[记录](verify/session-storage-stream-2026-10-02/evidence-secrets.txt)；该快照不覆盖随后追加的回执文字 |
| bash -n / 代码与文档 git diff --check | exit 0；原始测试输出保留 testify 的缩进与尾随空白，不改写执行证据 |
| 提交前 npm audit（web） | 镜像接口 404；临时使用官方 registry 后发现 2 项 high（brace-expansion / minimatch），均为本轮未改动的前端传递依赖；[记录](verify/session-storage-stream-2026-10-02/npm-audit.json)，未自动修改前端锁文件 |

依赖 vendor 为标准 `go mod vendor` 输出，含升级导致的旧 semconv 包删除，未手工修改第三方实现。新测试只访问显式配置的 loopback 服务，生产实现只改两个现有边界；没有增加原文持久化、新执行入口或绕过工具操作阻断。KxMemory 不可用，本地报告与方案承担连续性。

## 下一轮与交付边界

按 [分布式一致性修复方案](../design/2026-10-02-session-distributed-coherence-plan.md) 实现跨层版本绑定、所有写入口 fencing/CAS 与 L1 验证，再增加完整 handler 多实例/持久化延迟的失败派发用例。SSE 首字语义保持终态缓冲；下一轮另测 live heap/RSS、并发与还原后扩张。部分 v4 可选元数据仍使用历史省略编码，本次没有宣称全字段 replacement 已完成。

本轮改动保存在当前 chat 附着的隔离工作区。审计完成时尚未提交或推送；用户随后要求提交并推送，本轮交付采用独立分支，main 合并另行处理。没有改变原主工作区的并发未提交修改。清理前本轮独立 Redis DBSIZE=0、PG 除三库默认数据库外新增数据库数=0，随后删除本轮两个初始容器及匿名卷；runner 各次创建的容器也通过 EXIT trap 清理。无标签业务秘密、媒体/OCR/ASR、未知扩展、原始日志与 Redis 明文字典保留期仍属未完成的独立治理范围。
