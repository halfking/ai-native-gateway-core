# R92 域A 报告：canonical 视图契约与迁移 815/816 链（第三十二轮）

审计人：分域子代理 A（只读）。HEAD=8f79ad402（审计起点），全程 git show 对账 + 真库只读/事务内验证。

## 一、逐提交判定

| 提交 | 判定 | 证据（file:line） |
|---|---|---|
| b28ad0c98 §9.46 迁移816 | **风险**（主体成立，安全声明被真库证伪，见 P1-1） | 守卫正则 `^[0-9a-fA-F:.]+$` 放行 `'999.999.999.999'`/`'deadbeef'`，`::inet` 照样 22P02；816 up 本体、幂等判据、fail-closed 对账（816:318-342）本身一致 |
| cc13469d5 真库往返门 | 成立 | db/view_schema_v2_contract_test.go:493（重放链含816）、:730（先down816）、:815-824（确定性两段拆分）；TEST_PG_DSN 实跑 PASS |
| c856bfcfc 部署顺序 | 成立 | db/request_logs_view_schema.go:78 早退 `canonicalExists && bodyIsV2`，bodyIsV2 探测（:66-72）对 816/817 体均命中 → 无强制先后 |
| 641b4f99d 815 幂等门 | 成立 | 815 down 末尾订正「740 是例外」与 740.down 末行 DELETE 一致；up→down→up 门在 contract test :766-780，实跑 PASS |
| f7f1c97f4+f4977626e 接815 | 成立 | installer main.go:708-709/926、runner.go:654、TSV:222；installer 全模块测试绿 |
| ff4966e1e parity 门改指 | 成立 | 真库 relkind 实测：`session_bodies_with_current_month`='i'、`session_bodies_unified`='v'；loader.go:332 改指正确 |
| 1cc851438 / f1210a2eb / 17cd78e5f raw_model_name 族 | 成立（附 P3-2） | contract test :652-710 三臂各自正确期望（v1 臂钉真值 900001）；f1210a2eb 纯文档 |
| e89caddde 值层一致性门 | 成立（静态核验，未对活库重跑） | cmd/tools/validate_sessions_v2/ 门体在位 |
| 4624ea5c2 夹具丢 DEFAULT 记录 | 成立 | db/view_schema_v2_contract_test.go:886-897 现场注释（纯注释提交） |
| a0da9066d 816五点腿+admin权威源腿 | 成立 | embeddata 816 up/down 与 canonical md5 逐字节同（`834c5843…`/`454dca11…`）；main.go:711-712/927、runner.go:669、TSV:223（205行）；admin 权威源切 816 |

## 二、发现清单

**P1-1｜816 的 CASE 守卫不闭合**：`'999.999.999.999' ~ '^[0-9a-fA-F:.]+$'` = t 而 `::inet` = ERROR（deadbeef 同型）→ 单行畸形值打挂整条 canonical 视图每个读方，恰是 816 头部声称已消除的形态。migration_816_test.go:55-56 只钉 garbage/多跳XFF/空串三个良性样本。缓解：写侧 ParseIP 门已落（a0da9066d）+ 存量干净 + 本机库已被 817 覆盖。**修法已存在（pg_input_is_valid），须落回本仓**。

**P1-2｜integration 标签树编译红**：admin/request_logs_indirect_readers_test.go 无 build tag 却引用 `!integration` 文件的 v1DirectTables → `go vet -tags=integration ./admin/` exit 1；TestIntegrationTaggedTreeCompiles 连红。带 integration 标签的整棵真库门树不可编译；无标签路径全绿故静默。

**P2-1｜本机真库领先本仓一个迁移**：817（pg_input_is_valid，§9.64）在 schema_migrations（2026-10-02 22:54）而不在仓内任何分支。后果：816 幂等判据（816:86-90 要求 `WHEN t.client_ip ~` 片段）不认 817 体 → 816 重放会静默降级活库视图且 817 账本行仍在（跳过=永久降级）；通道台账无 `:815/:816` 行 → 下次通道运行必然重放 815（无害）与 816（降级）。

**P2-2｜（窗口前遗留）sql/schema TestDerivedBaselineLagIsSuppliedByMigrations 红**：三份 01-schema.sql 已被 d5932d26c cp 成同一文件，baselineLagObjects 声明的 10 个滞后对象现存在于 installer 副本。566/608/609 全 IF NOT EXISTS/OR REPLACE，fresh 不会真炸，属「声明滞后清单过期 + 门在 main 上红」。

**P3**：P3-1 816 down 回滚后仍留 816 口径 COMMENT（down:232-237）；P3-2 id 值层门会话两臂 NULL 断言夹具近似恒真（克隆无 DEFAULT 未种 id，4624ea5c2 的坑自家新门复犯）；P3-3 轮31 §四#13 列序漂移已消失（cp 收敛，代价即 P2-2）；P3-4 816 down 删账本行/815 down 保留，同链两套惯例（contract test 用显式 DELETE 补偿，无实害）。

## 三、核实为健康的面
816 五点同步四点完整且每点有门（TestCanonicalStartupMigrationsAtOrAbove704AreRegistered/TestStartupFilesAreAllEmbedded/TestStartupManifestMatchesStartupFiles/TestDownMigrationsMirrorCanonicalSources 全绿）；三基线副本字节一致；816 down 真可逆（确定性 proj 重建、118 列 fail-closed、无 DROP VIEW）；DSN 真库契约套件绿（scratch 库）；S4 停写分类已随 816 同步（stats_minute_rollup 条目「已作废」标注）；真库 816 状态健康（已入账、118 列、client_ip 第115位 inet）。

## 四、不确定项
1. 817 出处（哪个会话、是否合入 main）——决定 P1-1 修法与 P2-1 降级窗口。
2. e89caddde 值层门未对活库重跑（P1-2 修复前 integration 树被阻塞）。
