# hotzone P1+P2 批判式审计(2026-09-30)

- 审计对象: docs/storage/2026-09-24-hotzone-dual-mode-plan.md 的 P1(H1)+P2(H4) 交付
- 提交谱系: `ced9eb9b1`(H1 索引+H4 静态件) → `9b9061276`/`87dbead05`/`31e468a66`(接线三件,09-28 轮) → 后继加固 `964c0060a`(21 轮)、`d141c8036`、`4ba3a3be3`(20 轮) → 本轮补齐 `72436bac8`(H1 基准)
- 方法: 逐验收项对照代码锚点 + `git log/show` 提交考古 + HEAD=72436bac8 干净 worktree 全量回归(主树被并行会话的未跟踪 WIP 污染,bg/cmd 包不可构建,回归按本仓"脏树走临时 worktree"先例执行)
- 状态: P1+P2 关闭;P3(H2 full 装配)/P4(H3 镜像接线)/P5(指标+文档) 未开工

---

## 一、结论

交付成立:H1/H4 全部验收项有代码锚点与测试,且交付后经三个独立审计轮实质加固——
其中 21 轮修掉了 09-28 轮交付物里的一个真实 P1 级记账缺陷(F1)。09-28 轮的
"9 项 lite 套件全绿"声明为真,但该轮自写测试未覆盖两相记账交互,缺陷由后继
轮的测试(`TestHotZoneTrimmerExpiryUpdatesQuota`)钉死。方法论教训见 §三 F1。

## 二、验收矩阵(逐条带锚点,当前 HEAD 实读)

| 项 | 方案验收要求 | 锚点 | 判定 |
|---|---|---|---|
| H1-1 | `index map[string]*indexEntry` 与 sizeUsed 同受 fc.mu | cache_v2_file.go:65/84 | ✅ |
| H1-2 | Set 全程持锁(含登记索引);Delete/removeExpired/removeIfUnchanged/ensureSpaceLocked 淘汰同步摘除 | Set fc.mu.Lock 全程(:317)+四处 delete(fc.index) | ✅ ensureSpaceLocked 内无二次加锁 |
| H1-3 | 启动 Walk 填充索引;Get 命中跳过 Stat,不一致按 miss 摘除 | NewFileCache Walk(:128);Get indexed 分支零 os.Stat(:216-241) | ✅ 静态可证 |
| H1-4 | 索引一致性测试(并发交错后索引==磁盘) | TestFileCacheIndexConsistency(:462,双对账含 sizeUsed)+IndexSurvivesRestart(:530) | ✅ |
| H1-5 | 基准测试证明 Get 热路径减 1 次 Stat | BenchmarkFileCacheGet(72436bac8) | ✅ 本轮补齐(原缺失,见 F3) |
| H4-1 | 三 spec 默认 true/1GB/7h,范围 1–100/1–168 | spec_storage.go:164/175/189 | ✅ |
| H4-2 | HotZoneTrimmer 30 分钟、先删最旧 | defaultHotZoneTrimInterval(:34);两相均 mtime 升序(:273/298) | ✅(遍历范围有合理偏离,见 F4) |
| H4-3 | FileCache.ResizeMax | cache_v2_file.go:153(内部自持 fc.mu,nil-safe) | ✅ |
| H4-4 | 热重载:settings 变更 ≤1 轮询周期生效 | WithReload 每 tick 直查(bg:95-107);GetPlatform* 无缓存(helpers.go:8-20 实证);hotconfig 只轮 `llmgw_%` 前缀,storage.hotzone_* 不在视野 | ✅ 接线方式据此定 |
| H4-5 | 装配接线 | initStorageMode AND 门控(storage_mode_init.go:295,31e468a66) | ✅ cmd 子集测试绿 |

## 三、发现表

| # | 严重度 | 发现 | 处置 |
|---|---|---|---|
| F1 | P1(已由 21 轮修复) | **09-28 交付的 TrimOnce 过期相不回扣 `onDiskBytes`** → 配额相以删除前总量判定,超删未过期文件。实锤场景:`TestHotZoneTrimmerExpiryUpdatesQuota`(quota=10,两个 9B 文件,删 1 个过期后剩 9B≤10B 应全保留;原实现会把 fresh 文件一并删掉) | 已修(964c0060a/d141c8036 谱系,现 :285 逐删回扣)+测试钉死。教训:分相共享同一计数器时,每删一个必须立即回扣,否则下一相以脏总量判定;本轮"套件全绿"为真但不证明无此缺陷——测试覆盖面与实现缺陷正交 |
| F2 | 声明失实(提交信息) | ced9eb9b1 提交信息称 "index_entries Prometheus gauge in monitoring/storage_metrics.go"——grep 实证 monitoring 包无此 gauge;实际是 `FileCache.Stats()` map 字段经 /metrics/storage 端点 file_cache 段透出(storage_metrics_endpoint.go:60) | 可观测性成立、命名失实,本审计留档不回改历史提交信息;与 20 轮对 ResizeMax"缩容交由 trimmer"失实描述的订正(4ba3a3be3)同型 |
| F3 | 验收缺失(本轮补齐) | H1 验收"基准测试"从未交付(cache_v2_file_test.go 全文件零 Benchmark) | `72436bac8` 补 BenchmarkFileCacheGet(index_hit/index_miss_absent)。口径:hit 22.3µs(含 ReadFile+解析)vs miss-absent 1.7µs(Stat 即失败),"减 1 次 Stat"由静态代码路径承载而非时延差;系统调用级计数需 dtruss,macOS SIP 阻断,不做机器级断言 |
| F4 | 语义偏离(判定合理) | 目标原文"遍历整个 HotZone.Dir"已改为**三个受管子树白名单**(cache/session_bodies/requests,d141c8036) | 偏离方向安全:整树遍历会按配额删除 root 下未知文件(运维放置物/未来未登记子树);白名单+symlink 拒绝+OpenRoot/SameFile TOCTOU 防护,三组测试钉死(OnlyManagedSubtrees/SkipsSymlinkedSubtree/SkipsNestedSymlink)。方案 §8 已注记修订口径 |
| F5 | 挂账(不重复修) | GetPlatformInt 不做 spec Min/Max 钳制,settings_kv 直写 0 可达 | 964c0060a 已在 worker 侧 fail-safe 兜底(retention/quotaEnabled 守卫);getPlatformInt 全局钳制影响全部消费方,21 轮已登记 owner 决策,本审计不重复处置 |

## 四、回归证据(HEAD=72436bac8,干净 worktree /tmp/hotzone-audit-72436bac8)

```
go test -race -count=1 ./storage/ -run "TestLiteConsistency|TestLiteReconcile|TestLiteBody|TestLiteRepair|TestRepairDoubleConfirm|TestRepairGrace|TestRepairDeleteWithout" -v
  → 9/9 PASS(ok 1.9s 主树复跑;worktree 计数 9)
go test -race -count=1 ./bg/ ./config/ ./domains/session/v2/
  → ok bg 27.5s / ok config 1.6s / ok v2 3.3s
go test -race -count=1 ./cmd/gateway/ -run "TestInitStorageMode|TestStorageRuntime|TestResolveCacheTrimRetention|TestHotZone"
  → ok 2.1s
go vet(四包,worktree 内) → 净
go test -bench BenchmarkFileCacheGet -benchtime 200x → 两子基准 PASS(数字见 F3)
```

注:主树 bg/cmd 构建失败系并行会话未跟踪 WIP(`bg/session_turn_details_expired_metrics_test.go` 引用未定义 SessionTurnDetailsExpiredHotStats 等),与本任务无关,未触碰。

## 五、遗留风险与挂账

1. **F5 全局钳制** owner 未决(21 轮登记)。
2. **索引外部删除漂移仅在按键访问时自愈**(fail-open 摘除):bg.CacheTrimmer 绕过索引删文件后,索引残留条目驻留至该键下次 Get/淘汰,内存量级=键基数,按"加速视图"不变式接受。
3. **`storage.hotzone_enabled=false` 运行期语义仅=停清**(lite 的 L1.5 照常运行):"回退历史装配"只对 full(P3)有意义,P3 实施时需把"运行期关开关是否卸载 full 热区"定义为显式行为。
4. **data/hotzone 今日无写入方**(H2/H3 未接线),trimmer/预算/热重载链路均为空转热身;首次真实流量前的验收(P5)仍欠。
5. 主树存在并行会话 WIP 未跟踪文件,任何全包测试须走干净 worktree。

## 六、Handoff(下一轮入口)

- P3(H2 full 装配,≈0.5 天):读链已就绪(da6b95627 契约翻转:full+注 fileCache 即共享 L1.5);仅欠 initStorageMode full 分支创建 hotZoneOnly runtime + `NewSessionCacheV2WithMode(full, fileCache)`;注意 §五.3 开关语义。
- P4(H3 镜像接线,≈1.5 天):storage/file/request_mirror.go 编码层就绪(TODO(wiring-pending) 三项);接线点=SessionBodiesWriter 三方法调用方 + telemetry request_logs_bodies_hot 落库处(方案 §3-H3 修正版)。
- P5(≈1 天):monitoring hotzone 维度分列(顺带把 F2 的 file_cache 段口径写清)+ docs/storage README/deployment-guide 配置矩阵 + 全量回归。
- 推送纪律:小步提交、`git -c rebase.autoStash=true pull --rebase` 后推、远程竞争激烈;主树脏(并行 WIP)→ 构建/测试一律临时 worktree。
