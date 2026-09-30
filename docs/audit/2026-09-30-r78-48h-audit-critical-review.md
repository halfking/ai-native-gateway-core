# 48h 修订审计 · 三十八轮（R78）批判式复审 — 2026-09-30

> 窗口：`2026-09-28 13:47` .. `2026-09-30 13:47`（48h），共 **248 提交**。
> 审计基线：`origin/main` = `f44d1e045`（本轮开工前先 `git pull --ff-only`，本地落后 1 提交）。
> 方法：六域只读子代理并行取证 + 主代理**逐条复验**（不接受子代理与提交信息的断言本身）。

---

## 0. 一句话结论

本轮**没有发现 48h 窗口内的功能性回归**；HEAD 的 hotzone 镜像 F-A/F-B/L1.5 三项经
代码与还原双向核验为真。真正的问题在**别处**：

1. 一处**高危真缺陷**（`admin/tenants.go` 五路聚合静默造零 + 存在性检查把 DB 错误
   报成 404），属 R36-A2「只收了 1/5」的漏账，本轮根修。
2. 两处**声称与代码不符**（`46acf0a1d` 的「不再丢行」是惰性的；`c86c79dfd` 的
   「internal/ir 全包绿」实为运气），本轮留档并根修其一的真实后果。
3. 两处**推送门假绿/假红**（`log_skip` 未定义；`.enc` 存在性按「全有或全无」判定），
   会真实阻断 GitHub 推送，本轮根修。
4. 分支审计结论为**空集但需留证**：84 个远端分支**全部已并入 main**，
   1h–48h 窗口内**零个**未合并子分支，故「合并/删除」无事可做。

---

## 1. 分支审计（48h / >1h 不活跃未合并子分支）

| 项 | 实测 |
|---|---|
| 远端分支总数 | 84 |
| **未并入 `origin/main` 的分支** | **0**（`git branch -r --no-merged origin/main` 空输出） |
| 1h–48h 窗口内的未合并分支 | **0**（窗口内**任何**分支都不存在） |
| 最新的未合并分支 | 无；已并入分支中最旧的一批停留在 2026-09-12 |
| `git fetch --prune` 结果 | 远端已删除 30 个分支，本地引用同步清理 |

**判据留证**：`git for-each-ref refs/remotes/origin` + `git branch -r --no-merged
origin/main`（权威口径）。**不要用 `git merge-base --is-ancestor` 在 PowerShell
管道里逐个跑**——`$LASTEXITCODE` 在该上下文下不可靠，本轮首次尝试即产出
「main 自己未合并」这类假阳性，改用 `--no-merged` 后结论反转。

**登记不修**：84 个已合并分支（`docs/necessity-gate-round*`、`backup/*` 等）保留。
它们已无独有提交，删除属纯整理动作且不可逆，不在本轮授权范围内。

---

## 2. 提交流总结（248 提交）与批判结论

按 scope 统计（`git log --pretty=%s` 首段）：

| 类别 | 数量 | 判读 |
|---|---|---|
| `audit` / 纯文档 / `merge` / playbook | 67 + 43 + 18 + 14 = **142** | 约 **57%** 的提交是自我记账，非代码 |
| `storage` | 8 | hotzone 镜像主线，HEAD 收口 |
| `ir` | 6 | IR 序列化 / reasoning_effort 收窄 |
| `web` / `dashboard` | 8 | 看板 + 统计页 + 日历组件 |
| `scripts` / `tests` / `installer` / `secrets` | 12 | 推送门与安装器 |
| `streaming` | 4 | R-vapeur3 终止帧 |
| 其余（admin/bg/autoroute/proxy/dispatch…） | ~30 | 分散修复 |

**批判性观察（登记）**：提交流已明显**文档化过载**——每轮的平均「审计文档
字数 / 代码行数」比值极高，而真正被代码证伪的断言（本轮抓到 2 条）恰恰都出现在
这些长提交信息里。**建议**：下轮起，提交信息的「验证结论」段必须附**可重放命令**
（子代理已按此纪律复核，并因此证伪了自己的初判，见 §4）。

---

## 3. 已核验为真的 48h 声明（不必再审）

| 声明 | 核验方式 | 结论 |
|---|---|---|
| `f44d1e045` F-A：镜像投递挪至 `tx.Commit` 之后 | `session_writer_v2.go:762` Commit → `:765` `committed=true` → `:775-780` 投递；10 条 commit 前 return 路径全部先于投递 | **真** |
| F-B e2e：真 `RequestMirror` + 磁盘三件套断言 | 断言 `requests/{tenant}/{date}/` 下 3 文件、gunzip 内容、`Len(entries,3)` 闭合；`Close()` 排空队列故不 flaky | **真**（路径/参数守门，非顺序守门；顺序由 `CommitFailsNoMirror` 覆盖） |
| L1.5（R-C）：full 模式有真实写路径 | `cache_v2.go` L3 回填/Set 均 mode-blind | **真** |
| `3efae9985` 764 索引建在分区父表 | `public.request_logs`（非单分区），可传播至 ATTACHED 与未来 `PARTITION OF` | **真**（DETACHED 壳 337 期间不受益，705 重挂自愈——「级联全部现存分区」措辞略宽） |
| `c8c102698` 763 fail-closed | `DO $pk$` 无 `EXCEPTION`、无 `NOT VALID`、DDL 顶层；`setval` 守卫 `is_called` 感知且不回退 | **真**（fail-closed，非 all-or-nothing，见 §5-R2） |
| `d0d2c9eb4` R35-A1 egress 缓存不再毒化 DB 错误 | 错误路径在 `:113` 早于缓存写入 `:121` 返回；键为 `providerID`（全局 PK） | **真**，无跨租户泄漏 |
| paramguard 守门**非**自证 | 守门持有字面量 `upstreamRejectedGLMEfforts`/`supported`，不从 `reasoncap` 读取 | **真**（独立性成立；文件头「不读生产代码」措辞略宽，见 §5-R5） |

---

## 4. 被证伪 / 打折的声明（本轮留档）

### F1（已根修）`admin/tenants.go` 五路聚合静默造零 —— **高危**

R36-A2 只给 `daily` 一路加了 `slog.Warn`，其余四路原样。实测缺陷：

- `:685` 存在性检查 `_ =` 丢弃错误 → **任何** DB 失败（超时/断连/57014）让
  `exists` 停在零值 → 对**存在的租户**返回 **404 tenant not found**。
  DB 错误被伪装成「租户不存在」，调用方会把假 404 缓存下来。
- `:739`/`:747` 两个 totals 用 `_ =` → 上下文耗尽时 `TotalRequests=0`、
  `TotalCredits=0`，而后续查询若侥幸成功则产出**自相矛盾的 200**。
- `:766`/`:792` `if err == nil` 无 else → byModel/byApp 静默空数组。
- `:825` `ts >= now() - days` 与 Shanghai 钉死的 `generate_series` 起点不同源，
  最老一天桶被截断（`days=1` 时 LEFT JOIN 静默丢弃当日 0 点前的行）——**登记不修**。

**危害链**：对账与计费都读这个端点。「无流量」与「查询被杀掉」返回同一个 200，
前者是业务事实、后者是故障，二者不可区分。

**根修**（本轮）：五路全部改为显式失败；新增 `writeTenantStatsError`，按
`context.DeadlineExceeded` 分流 **504**（提示收窄 `days`）与 **500**，
`daily` 的 scan / rows.Err() 中途失败也升级为显式错误而非静默截断。

### F2（已根修）`46acf0a1d` 的「空流 gate `[DONE]`-break 不再丢行」是惰性的

`:341` 已**无条件** `buffered = append(buffered, line)`，而 `:355` 又把**同一行**
append 一次。`:350` 分支在 `[DONE]` 负载下可达时会向线上吐**两个
`data: [DONE]`**。当前该分支不可达（所有内容行在 `:362` 先 return），故线上无
实际故障——但这是引信，且提交信息把「丢行」讲反了。

**根修**：删除 `:355` 的重复 append，只留 `break`。`hasCombinedDone` 分支之所以
要 append 合成行，是因为那里的 `line` 已被剥掉 `[DONE]`；此处 `line` 自带
`[DONE]` 且已在缓冲中。R-vapeur3 复现 harness 4 形态 5 用例回归全绿。

### F3（已根修）`c86c79dfd` 的「internal/ir 全包绿」不成立

`TestReportAnomaly_DedupSweepTimeGate` 在 HEAD **红**。追因：断言比较两次
`time.Now()`（`lastSweepAt.Equal(firstSweep)`），而本测试整体耗时 <1ms，
**低于 Windows 默认时钟粒度（~15.6ms）**——同 tick 时假红。实测连跑 3 次：
**红/红/绿**，确证为 flake 而非稳定失败。该测试最后修改于 `153ad093a`(09-25)，
**在 48h 窗口外**，属既存缺陷；但它使 09-30 那条「全包绿」声明不可复现。

**根修**：断言改为与**回拨值**比较（`lastSweepAt.After(rewoundAt)`），
与时钟粒度无关。同因的 `TestReportAnomaly_DedupTTL` 一并根修。回归 **8/8 绿**。

### F4（已根修）`tests/deploy_sops_test.sh` 两处推送门缺陷

- `log_skip` 在 `:153` 被调用但**本文件从未定义**（对照
  `deploy_cli_test.sh:55` / `env_injector_test.sh:55` 均有定义）。本脚本用
  `set -uo pipefail`（**无** `-e`），所以坏调用只打印 `command not found`
  后继续——这正是它长期隐形的原因。**根修**：补齐助手 + `TESTS_SKIPPED` 计数。
- `.enc` 存在性判定是「**全有或全无**」：`:105` 要求两个文件**都**缺席才跳过，
  否则落到两条 `assert_file_exists` → 持有**恰好一个** `.enc` 的操作者
  （53971b270 之后这就是常态）会被判 FAIL，**阻断每一次 GitHub 推送**。
  **根修**：改为逐文件判定；`scan_secrets_skips_enc` 在两个 `.enc` 都缺席时
  如实 SKIP（扫两个不存在的路径恒 rc=0，是假绿）。
  回归：`bash tests/deploy_sops_test.sh` → **22 passed, 0 failed**。

### F5（已根修）`763_provider_events_contract.down.sql` 不撤回台账行

up 在同事务盖 `'763'`，down 不删 → 回滚后台账仍宣称契约在位而 PK/序列/索引已
消失。**根修**：down 末尾补 `DELETE FROM public.schema_migrations WHERE version='763'`。

### 证伪的**子代理初判**（留档，防复发）

子代理报「`unified_probe_status_views_test.sh:27` 顶层 `local` + `set -e` 立即
中止，DSN 回退是死代码」。主代理实测：`:27` 的 `local_password` 是**变量名**，
不是 `local` 内建；`bash -c` 实证脚本正常跑到 `SCRIPT_END`，exit 0。
**该发现作废**。教训：把变量名当关键字读，是子代理式审计的典型误报——
凡涉 shell 语义，必须实跑一次再登记。

---

## 5. 登记不修 / 移交

| # | 项 | 判据 |
|---|---|---|
| R1 | 14 处历史迁移号撞号（350/375/388/389/391/394/399/400/402/407×3/431/432/478/491） | 已被 `migration_version_unique_test.go` 以 `<492` 显式豁免并**审计留档**。`schema_migrations` 按 `version` 键（`ON CONFLICT (version) DO NOTHING`），撞号确实会导致**同号第二个文件永不施加**——`db.go:606-611` 的 471 事故即此机制。**本轮不动**：对已在真库落账的历史迁移改号，正是 471 事故的成因，风险远大于收益。759–764 区间已唯一且单调。 |
| R2 | 763 的「原子性由调用方包裹」在 `psql_file()` 通道不成立 | installer 走 `--single-transaction` 故真；另一通道 autocommit 下，fail-closed 中止会留下「序列+默认值已提交、无 PK、无台账行」。改迁移施加语义风险过高，**移交**。 |
| R3 | `storage/file/async_writer.go` rename 后无父目录 fsync | 掉电可丢目录项，而该路径的立身之本是灾备取数。`MoveFileEx` 未带 `MOVEFILE_WRITE_THROUGH`。**登记**（跨平台，改动面大）。 |
| R4 | `RecordMirrorWrite(true)` 把「ID 非法跳过」计成写**错误** | `mirror_write_errors` 虚高，且对账无法区分「跳过」与「I/O 失败」；含 `/`、`\` 的 sessionID 会让 final_full 镜像**静默不落**。需新增独立 skip 计数——改指标语义，**移交**。 |
| R5 | paramguard 方言门可被整体绕过 | 能力档位取自**模型名**，但 `fixReasoningEffort` 仅在 `paramreg.Resolve(...)==DialectGLM` 时生效；经 OpenAI 形状中继（如实测所用 ark coding 端点）解析为非 GLM 方言 → `none`/`minimal` 原样透传 → 400。守门只测了 `dialect==GLM`。**移交**（需单独立项）。 |
| R6 | `env_injector_test.sh` AC-I2/I3/I4/I6 四条准则在 CI 全程跳过 | 跳过本身**策略正确**（未被跟踪=新常态），但这四条验收准则在 CI 从未真正执行。**登记**（需 fixture 才能恢复）。 |
| R7 | `proxy/egress_provider.go` `Invalidate` 无生产调用方 | 运维改 profile 后要等 60s TTL 才对 R35-A1 的缓存生效。**登记**。 |
| R8 | `admin/user_usage_stats.go` `tenantID != "default"` 跳过租户过滤 | 「default」租户管理员可见全部租户用量。既存仓 idiom（`user_profile.go:93`）被传播到新端点，**需裁决**。 |
| R9 | `domains/attachments` 两个测试在 Windows 必红 | `TestSafeJoin_DirectoryTraversal` / `TestIRTransformer_...` 断言正斜杠后缀，而生产用 `filepath.Join` 出平台原生分隔符。**非 48h 回归**（生产代码 09-29 的改动与路径构造无关，仅 3 行且不涉 join）。真机 Linux 绿。顺带暴露一个**潜在**数据可移植性问题：`meta.Path` 落库时携带平台分隔符，跨平台读取会失配——生产为 Linux，**登记**。 |

---

## 6. 本轮改动文件

| 文件 | 性质 |
|---|---|
| `admin/tenants.go` | F1 根修：五路聚合 + 存在性检查显式失败；新增 `writeTenantStatsError` |
| `domains/streaming/stream.go` | F2 根修：删除重复 append，消除潜在双 `[DONE]` |
| `internal/ir/anomaly_dedup_ttl_test.go` | F3 根修：断言去时钟粒度依赖（含同因姊妹用例） |
| `tests/deploy_sops_test.sh` | F4 根修：补 `log_skip`；`.enc` 逐文件判定；扫描假绿改 SKIP |
| `sql/migrations/startup/763_provider_events_contract.down.sql` | F5 根修：down 撤回台账行 |

## 7. 测试

```
go build ./...                                             → exit 0
go build ./admin/ ./domains/streaming/                     → exit 0
gofmt -l <本轮 4 个 go 文件>                                 → 净（ir 测试文件已 -w）
bash -n tests/deploy_sops_test.sh                          → exit 0
bash tests/deploy_sops_test.sh                             → 22 passed, 0 failed
go test ./internal/ir/ -run TestReportAnomaly_DedupSweep -count=1 ×8 → 8/8 ok（修前 2/3 红）
go test ./domains/streaming/ -count=1                      → ok
```

**环境态失败（与本轮零关联，登记）**：`go test ./...` 在本机（Windows，无
Redis / 无 PG / 无 Docker）有大批失败——`TestRedisRPMLimiter*`、
`TestLiteRequestLogSink_*`、`TestFileWriter_*`、`TestSelectP99Under1ms`、
`TestDiscoverGateways` 等，均为依赖外部服务的环境态套件。**未逐个归因**，
下一轮若在 Linux + 依赖齐备环境复跑，应先取该基线再判定。

`go vet ./...` 本机 >300s 未完成（超时），改用**按包定向 vet**
（`./admin/`、`./internal/ir/`、`./domains/streaming/` 均净）。
