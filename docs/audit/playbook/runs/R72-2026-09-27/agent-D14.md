# D14 安全横切 + installer 守卫恒查 子代理报告（窗口：949ec2f70..24c5c545a）

窗口 13 commits，改动面并集 89 文件（`git diff --name-only` 亲取）。本报告只读不改，所有行号以 origin/main HEAD=24c5c545a 为准。路径根 = `C:\workspace\llm-gateway-go\`。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **settings spec 长描述与 753 终稿语义相悖（窗口内新增后未随终稿修正回改）**。DescriptionLong 声称删除谓词是"expires_at 早于「当前时刻 − 本值」的行"——这正是被 14d34867f 批判式审计证伪的双 TTL 语义；终稿实为"到期即删"（`expires_at < NOW()`，保留期写入时烘焙）。又声称"迁移 753 另在函数内对入参做 GREATEST(...,1) 与 NULL→24 兜底"——终稿函数实为越界/NULL 直接 `RAISE EXCEPTION`（fail-closed 联锁），钳制在 Go 侧两处。触发路径：管理员在 settings 面读该长描述，误判"调小 TTL 会立即作用于存量行"（实际只影响新写入行，存量行按写入时烘焙的到期时间清扫），且误判 SQL 函数会静默钳制（实际靠 Go clamp 兜住、SQL 侧越界即抛）。同块注释"默认 24——与原硬编码行为逐字节一致，上线不改变任何既有行为"的前半句被同一审计证伪（清理从未存在过，上线是首次真正开始清扫 >24h 行为） | `settings/spec_lifecycle.go:50`（长描述）与 `:41-47`（注释块）；对照 `sql/migrations/startup/753_session_turn_logs_ttl.sql:61-74`（RAISE 联锁+到期即删）、`bg/partition_manager.go:480-488`、`domains/session/v2/turn_logs_writer.go:64-73` | 回改 DescriptionLong 为"到期即删（写入时烘焙）+ Go 侧 clamp 1..168 + SQL 侧越界 RAISE；调档只影响新写入行"；顺带修正注释块。属文案/文档漂移，无功能缺陷 |
| 2 | P3 | **白名单自清洁守卫对"注释残留型失配"结构性失效**。TestSQLReadGuardWhitelistCurrent 用 `len(//遍历)==0 && len(--遍历)==0` 双腿与判定：Go 文件的纯 `//` 注释命中在 `--` 遍历里不被豁免（该遍历只豁免 `--` 前缀），恒计数 ≥1，故与条件永不成立——Go 文件只剩注释命中时永不报"条目已无命中"。实例（3 条按设计意图应移除的存量条目）：session_export.go 已双腿化为 `request_logs_with_current_month` 视图（regex 唯一命中在其 ：200 注释行）、backfill_sessions_v2_v2/main.go 唯一命中在 ：1 文档注释、derive.go 命中在 ：2/:8 注释。触发路径：债务清偿后 DEBT/TOOLING 条目静默滞留——与该测试注释自述的"防白名单债务隐身"目的相反方向失效。注：R71 轮文档"全绿（含白名单自清洁）"的声明成立，但绿的原因部分依赖此盲区；三滞留条目窗口前已存在，非窗口引入 | `internal/sqlreadguard/guard_test.go:214`（判定逻辑）、`:58/:60/:77`（滞留条目）；`admin/session_export.go:200,216`、`cmd/tools/backfill_sessions_v2_v2/main.go:1`、`cmd/tools/backfill_session_bodies/derive.go:2,8` | Go 文件只走 `//` 遍历、SQL 文件只走 `--` 遍历做失配判定；或维持现行为但删除 3 条滞留条目并登记该盲区 |

无 P0/P1/P2 候选。

## 二、核实为健康的面

- **753 五点同步①-⑤逐点亲验全绿**：① `sql/migrations/startup/753_session_turn_logs_ttl.sql` 存在（另有 .down.sql）；② embeddata 副本存在且 `diff` bytes 级 IDENTICAL；③ `installer/cmd/llm-gw-installer/main.go:556-557` go:embed var `sessionTurnLogsTTLMigration753`；④ 同文件 ：727 embeddedSQLFiles map 条目（key `startup/753_session_turn_logs_ttl.sql`）；⑤ `installer/internal/dbinit/runner.go:312` StartupFiles 登记（750/751/752 在 ：288/:296/:304）。**750/751/752 复验**：embeddata 副本均 bytes 级 IDENTICAL，embed var :547-554、map :724-726、runner 登记齐全。
- **守卫测试断言范围亲读（覆盖 753 的证据）**：`TestStatsStartupMigrationsMatchCanonicalSources` 的 parity map 窗口内新增 753 条目（`installer/cmd/llm-gw-installer/stats_migrations_test.go:235-239`）钉死 embeddata↔canonical bytes 相等；`TestStartupFilesAreAllEmbedded`（:376-411）双向断言 StartupFiles⊆setupSQLDir（防"登记了没 embed"）+ embeddata ReadDir⊆StartupFiles（防"embed 了没登记"，.down.sql 按设计豁免）；`TestCanonicalStartupMigrationsAtOrAbove704AreRegistered`（:459-500）动态扫 canonical ≥704 全登记。**R71 F1b 749 豁免补登**（:441-449）正当：749 实测含 5 处 CONCURRENTLY；800 号既有条目不受豁免影响且已登记（runner.go:317、main.go:559/728）。
- **sqlreadguard 守卫当前为绿（静态复刻全仓扫描）**：Go 侧 56 个含 regex 命中文件中，41 个在白名单；15 个未白名单文件的命中逐文件亲读全为注释行或 inline marker（`bg/integrity_fingerprint_probe.go:51` 用 `sqlreadguard:allow` 显式豁免）。SQL 对象面：12 个命中文件中 update_api_key_model_cost.sql 纯注释不计，其余 11 个全在白名单且各仍有实命中（自清洁 SQL 侧不失配）。
- **白名单窗口改动最小化**：`internal/sqlreadguard/guard_test.go` 窗口 diff = gofmt 对齐 + 唯一功能性新条目 `admin/session_detail_v2.go`（LEGIT，R71 反向臂母表腿），无搭车放宽。
- **反向臂双腿化无 D14 安全面**：`admin/session_detail_v2.go:516-531` hot∪母表两腿均带 `tenant_id = $1 AND gw_session_id = $2` 全参数化（租户谓词无丢失），外层 `sessions s.tenant_id = $1`；`LIMIT 2` 歧义短路防同租户铺行放大扫描；歧义/未找到走哨兵错误（:563/:554，409/404），含 tenant_id 的细节仅入 `slog.Warn`（:558-559），客户端不回显内部错误串。
- **753 SQL 本体注入面健康**：无动态 SQL/EXECUTE/SECURITY DEFINER；p_ttl_hours 只进 RAISE/NOTICE 消息（plpgsql % 替换，非注入面）；DELETE 谓词 `expires_at < NOW()` 走 430 已建索引；ledger 自注册带 `to_regclass` 守卫（裸测试库与 installer 基座双环境可执行）；down 文件 `DROP FUNCTION IF EXISTS` + 簿记清理，并如实记录"回退=回到无清理状态"。
- **TTL 链路资源/降级安全**：Go 侧双 clamp（writer `domains/session/v2/turn_logs_writer.go:64-73`、caller `bg/partition_manager.go:480-488`）与 spec Min/Max 1..168 三处一致；清理调用全参数化（partition_manager.go:522-525），失败仅 slog.Error 不拖死进程，5min timeout，nil pool no-op，setting 每 tick 重读（HotReload 生效）。
- **aggregator flush（8a34eab76）**：三条语句常量化+全参数化；delete-by-id `ANY($1)` 精确覆盖读集（修复旧谓词重求值导致的未聚合先删静默丢失）；jsonb `||` 浅合并使 UPDATE/DELETE 间隙崩溃可幂等重放；无事务包裹但注释论证收敛性成立（`cmd/gateway/turn_logs_aggregator.go:129-149,243-249`）。（主代理注：该"收敛性成立"结论后被 D04#1 证伪——跨批 turn 键替换丢 stage，R72 已修。）
- **下一个可用迁移编号 = 754**：startup 最高 753（800 为既有特殊资产且已登记），operations/domain/manual/local 均低于 753；`scripts/apply-db-revision-sequence.sh` 末条 753；全仓无 754 占用；工作树 `git status` 干净。

## 三、未覆盖项与原因

- **go test 实跑（./internal/sqlreadguard/、installer 两包、涉并发包 -race）** —— 只读约束不写 GOCACHE，以同 regex 静态复刻扫描替代；守卫绿/安装包绿的最终裁决留主代理验证门实跑（R71 恒查清单项）。
- **753 是否已在存量真库实跑（迁移三纪律第 1 条）** —— 需真库凭据；sequence 脚本注释只证明 751 已 applied+verified 到 245 库，753 仅有编号空闲复核记录。
- **admin 包 500 回显存量债 85 处/22 文件（R71 L4）** —— 超出窗口，维持既有登记，本轮未重扫。
- **R47 DEBT 21 文件双腿化进度横扫** —— 窗口外存量债，本轮仅核窗口内新增读面（session_detail_v2 反向臂）与白名单一致性。
