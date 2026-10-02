# Handoff — 48h 修订审计 · 三十八轮（R78）批判式复审（2026-09-30）

> 完整取证见 `docs/audit/2026-09-30-r78-48h-audit-critical-review.md`。本文件只写
> 状态、改动、未决项与下一轮提示词。

## 1. 一句话状态

窗口内 **248 提交**、**零功能回归**；HEAD 的 hotzone F-A/F-B/L1.5 经代码与还原
双向核验为真。真正抓到的是**一处高危漏账**（`admin/tenants.go` 五路聚合静默造零 +
DB 错误被报成 404）、**两处声称与代码不符**、**两处推送门假绿/假红**。分支审计
结论为**空集且已留证**：84 个远端分支**全部已并入 main**，1h–48h 窗口内**零个**
未合并子分支，故「合并或删除」无事可做。

基线：`origin/main` = `f44d1e045`（开工先 `git pull --ff-only`，本地原落后 1 提交）。

## 0. 提交前合并复核（重要）

本轮收尾时 `origin/main` 又前进了 5 个提交（并入 `8257ff7bb`），其中
`ec5edcfd7` 做了**迁移 759→801 撞号修正**，与本轮 R1 同域。合并后复核结论：

- `admin/tenants.go` 自动合并成功，上游改动仅为 `logsTable` 字符串上的一条
  `sqlreadguard:allow` 注释，**与 F1 根修正交**；`writeTenantStatsError` 的 8 处
  调用点全部存活。
- 迁移号实测：`sql/migrations/startup/` 中 **≥492 区间零撞号**；
  759 = `759_report_snapshots_grain_dims.sql`、801 = `801_session_turn_details_duplicate_drain.sql`，
  各一。**R1 结论不变**：<492 区间仍有 14 处历史撞号（已被守门测试显式豁免）。
- 合并后重跑 `go build ./...` 与本轮定向回归，全绿（见 §3）。


## 2. 改动文件与关键行为

| 文件 | 关键行为 |
|---|---|
| `admin/tenants.go` | **五路聚合 + 存在性检查全部改为显式失败**。`:685` 存在性检查丢弃错误会让**任何** DB 失败对存在的租户返回 404——现在返回 500/504。totals/credits 的 `_ =` 造零、byModel/byApp 的 `if err == nil` 静默空数组、`daily` 的 warn-only，全部改为返回错误。新增 `writeTenantStatsError`：`context.DeadlineExceeded` → **504**（提示收窄 `days`），其余 → **500**；`daily` 的 scan 与 `rows.Err()` 中途失败也升级为显式错误。 |
| `domains/streaming/stream.go` | 删除 `:355` 对 `line` 的**重复 append**（`:341` 已无条件 append 过一次）。可达时会向线上吐两个 `data: [DONE]`。只留 `break`。 |
| `internal/ir/anomaly_dedup_ttl_test.go` | 两处断言由「与前一次 `time.Now()` 比较」改为「与**回拨值**比较」，消除低于 Windows ~15.6ms 时钟粒度的同 tick 假红。 |
| `tests/deploy_sops_test.sh` | 补 `log_skip` 助手（本文件调用但从未定义）；`.enc` 存在性由「全有或全无」改为**逐文件**判定（持有恰好一个 `.enc` 的操作者此前会被判 FAIL 并**阻断每一次 GitHub 推送**）；两个 `.enc` 都缺席时扫描用例如实 SKIP。 |
| `sql/migrations/startup/763_provider_events_contract.down.sql` | down 末尾补 `DELETE FROM schema_migrations WHERE version='763'`，与 up 的台账写入配对。 |

## 3. 测试命令与结果

```
go build ./...                                          → exit 0
go build ./admin/ ./domains/streaming/                  → exit 0
gofmt -l <本轮 4 个 .go>                                 → 净
bash -n tests/deploy_sops_test.sh                       → exit 0
bash tests/deploy_sops_test.sh                          → 22 passed, 0 failed
go test ./internal/ir/ -run TestReportAnomaly_DedupSweep -count=1 ×8 → 8/8 ok（修前 3 跑 2 红）
go test ./admin/ -run 'Tenant|tenant' -count=1          → ok
go test ./internal/ir/ -count=1                         → ok
go test ./domains/streaming/ -count=1                   → ok
```

`go vet` 改为**按包定向**（`./admin/`、`./internal/ir/`、`./domains/streaming/` 净）；
`go vet ./...` 本机 >300s 未完成。

**未归因的环境态失败**：`go test ./...` 在本机（Windows，无 Redis / 无 PG /
无 Docker）有大批失败（`TestRedisRPMLimiter*`、`TestLiteRequestLogSink_*`、
`TestFileWriter_*`、`TestSelectP99Under1ms`、`TestDiscoverGateways` 等），
均属依赖外部服务的套件，**与本轮零关联**，未逐个归因。

## 4. 遗留风险

1. **未验证生产行为**。本轮只过单测与 shell 套件，**未重建镜像、未起旁挂实例、
   未跑真库/真机 e2e**。`admin/tenants.go` 的改动把静默 200 变成 500/504——
   这在语义上正确，但意味着**原先被掩盖的慢查询会开始显式报错**。上线前应确认
   运维侧能正确呈现 504，而不是当成新故障。
2. **9 项登记不修**（详见审计文档 §5）。其中风险最高的三项：
   - **R1 历史迁移号撞号（14 处，<492）**：`schema_migrations` 按 `version` 键，
     撞号导致同号第二个文件**永不施加**（`db.go:606-611` 的 471 事故即此机制）。
     已被守门测试以 `<492` 显式豁免。**本轮刻意不动**——对已在真库落账的历史迁移
     改号正是 471 事故的成因。759–764 已唯一且单调。
   - **R2 763 原子性在 `psql_file()` 通道不成立**（autocommit 下 fail-closed 中止
     会留下半提交态）。改迁移施加语义风险过高，移交。
   - **R5 paramguard 方言门可被整体绕过**：能力档位取自模型名，但方言门决定是否
     生效；经 OpenAI 形状中继（**即实测所用的 ark coding 端点**）解析为非 GLM
     方言 → `none`/`minimal` 原样透传 → 400。守门只测了 `dialect==GLM`。
     上一轮 handoff 的「glm-5 收窄为 5 档」在这条路径上**保护不到**。
3. **提交流文档化过载**：142/248（57%）提交是审计文档/合并/记账。两条被证伪的断言
   都出自长提交信息。
4. **磁盘清理未做**：`domains/attachments` 两个测试在 Windows 必红（断言正斜杠，
   生产用 `filepath.Join` 出平台原生分隔符）。**非 48h 回归**，真机 Linux 绿。
   但它暴露一个潜在问题：`meta.Path` 落库携带平台分隔符，跨平台读取会失配。
5. **playbook CHANGELOG 本轮未追加**（该文件为超长单行密排 UTF-8，PowerShell
   写入有编码损坏风险，故不冒险改写）。本轮记账以本文 + 审计文档为准。

## 5. 下一轮提示词

- **先 fetch 再核**：`git fetch origin main && git log --oneline HEAD..origin/main`。
  本轮开局本地就落后 1 提交。
- **分支审计用 `git branch -r --no-merged origin/main`**，不要用
  `merge-base --is-ancestor` 在 PowerShell 管道里逐个跑——`$LASTEXITCODE` 在该
  上下文不可信，本轮首次尝试即产出「main 自己未合并」的假阳性。
- **凡涉 shell 语义，实跑一次再登记**。本轮证伪了一条子代理初判
  （把变量名 `local_password` 当成 `local` 内建）。凡涉「条件/断言是否可达」，
  先问「它现在可达吗」，再问「可达了会怎样」——本轮两条夸大声明（惰性 break、
  运气绿）都是可达性未验证。
- **若要闭合风险 1（部署验证）**：重建镜像 + 起旁挂实例（同 DSN/同 Redis、
  `LLM_GATEWAY_BG_MODE=data-plane`、备用端口，**不要重启 8782**），然后
  (a) 对 `GET /api/admin/tenants/{code}/stats?days=30` 人为制造慢查询，确认现在
  返回 **504** 而非静默零；(b) 看真实出站体确认版本（`version.json` 不随增量镜像
  更新，用 `go version -m <binary>` 或 `git_sha` 标签）。
- **若要动 R1（历史迁移撞号）**：先只做**只读盘点**——逐对确认同号两个文件在真库
  `schema_migrations` 里的实际 `description`，判断哪个真正落过账，产出「哪一份需要
  自愈通道补齐」的清单。**不要改号**。
- **若要补 R5（方言门绕过）**：先按实测路径取证——用 provider 34 凭据经网关
  请求一个 `glm-5.2`，确认 `paramreg.Resolve(catalog, protocol)` 在 ark coding
  通道上究竟解析成什么方言；再决定是扩表还是加守门。取证纪律：合法值 + 非法值
  双对照、间隔 7s、关键档 n≥3 复验。
- **提交信息纪律（下轮起）**：「验证结论」段必须附**可重放命令**。本轮所有被证伪
  的断言，都是因为没有可重放的验证步骤，下一轮才靠重新取证推翻。
