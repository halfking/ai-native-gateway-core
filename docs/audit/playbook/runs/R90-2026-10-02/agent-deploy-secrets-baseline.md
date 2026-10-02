# R90 只读审计报告 — 部署脚本 + secrets 门 + deploy baseline 域（HEAD=20fbdba74）

只读审计，未修改任何文件。主会话复核注：F1/F2/F3/F4/F5 已亲验属实；F1+F2 以"SSOT 角点同步 + 三向收敛"落地修复（含 fresh-install e2e 验证），F4 落地修复，F5 落地订正。

## 一、发现（候选，待主代理复核）

### F1（P1，域外既有、本轮实测坐实）：schema 镜像守卫当前是红的 —— `deploy/build-image.sh` 构建门被 §四.7 漂移直接阻断
- 证据：`bash scripts/verify-stats-schema-mirror.sh` 实跑 **rc=1**，输出 `schema mirror mismatch: sql/schema/01-schema.sql != deploy baseline`；该脚本第 7-11 行对 `sql/schema/01-schema.sql` vs `deploy/sql/schemas/baseline/01-schema.sql` 做 `cmp -s` 字节等检查；`deploy/build-image.sh:56` 以裸命令调用它（build-image.sh:13 `set -euo pipefail`）→ **镜像构建当前必失败**。
- 这正是 docs/12小时内修订审计-20261002-0435.md:92 预言的失败模式（守卫只此一处、平时无人跑）。建议级别：**P1**（不是 20fbdba74 引入，但本轮必须处置）。

### F2（P1→建议按 P1 复核）：deploy/sql/schemas/baseline + installer embeddata 双镜像缺失整个 566 governor 子系统
- 结构性 diff（SSOT vs baseline，CREATE/DROP/ALTER/COMMENT 级）共 27 行，全部指向一件事：`sql/migrations/startup/566_credentials_governor_revision.sql`（提交 47ff5d717，2026-08-23）从未回灌镜像：
  - 缺 `credentials.revision` 列、`credentials_governor_revision_seq` 序列、`credentials_revision_idx` 索引、`bump_credentials_governor_revision()`/`notify_credentials_governor_revision()` 两函数、`trg_bump_credentials_governor_revision`/`trg_notify_credentials_governor_revision_{insert,update}` 三触发器、`COMMENT ON credentials.revision`；
  - `trg_notify_auto_route_creds` 窄版（baseline:28883 / embeddata:28884）vs 宽版（sql/schema/01-schema.sql:29057，多六列）；
  - 另缺 2 条 `ALTER TABLE tenant_model_policies(_audit)` pkey、`api_key_auto_profile_pkey`（baseline 单侧缺）、2 处注释文本漂移、1 处 columnar_heal 函数排序差。
- 总量：30393 vs 30204 行；裸 diff 352 行。
- 判定：**不是纯历史快照，是选择性手工维护**——9fb81a7c5（2026-09-30）同时更新了三份拷贝，但 566 特性集始终无人回灌（47ff5d717 对 baseline/embeddata 触碰数为 0）。
- 主会话复核补充：三方各有所长——baseline 有宽版 self_check CHECK（644 形态），SSOT/embeddata 是旧窄版；embeddata 有 api_key_auto_profile_pkey 而 baseline 缺。双向漂移，无一方是"有意差异化"。

### F3（P2）：one-click / 离线包"纯 psql 三件套"部署形态下，窄版触发器是**真实可达的终态**
- 消费链：`deploy/one-click/lib/bootstrap.sh:59-62` + `scripts/build-offline-packages.sh:88`；`deploy/one-click/DEPLOYMENT-GUIDE.md:126,138-140` 明确指示运维手工 `psql -f sql/baseline/01-schema.sql`。若操作者不跑 gateway migrate/installer，则停留在窄版态：
  1. `domains/dispatch/policy_publisher.go:225` 的 `WHERE revision >= $1` 对缺列库每次 catch-up 抛 42703（仅 warn 不崩）→ governor 策略传播整体失效；
  2. 窄版触发器漏六列 → 仅改 rpm/tpm/fp_slot/queue 列时不 `pg_notify('auto_route_refresh')`，`bg/auto_route_realtime_listener.go` 不唤醒。影响有界：main.go:5311 → autoIndexRefresher 默认 5 分钟 rollup 兜底（bg/auto_index_refresher.go:41）→ 实时性从秒级退化为 ≤5 分钟。
- 对比：installer 路径 566 在 StartupFiles 里（main.go:212,749；runner.go:609-621 失败即断）→ 窄版只是单次安装内的瞬态。`deploy-local-sys.sh` 不消费 baseline（:256-258 用 SSOT 宽版）。

### F4（P3）：deploy/start/stop 锁的 EXIT trap 不覆盖信号致死，残留锁靠人工清理；db-attach 残留锁会**静默永久跳过**
- `scripts/deploy-local-sys.sh:132` 仅 `trap ... EXIT`：SIGTERM/SIGHUP/kill -9 时 trap 不执行 → `$RUN_DIR/.deploy.lock` 残留。非永久卡死：129 行 die 消息自带恢复指引，fail-closed。
- 更差的是 db-attach 侧：`scripts/deploy-local-db-attach.sh:35-38` 锁被占时 `exit 0` **静默跳过**——若锁残留（kill -9），定时任务永远 exit 0、marker 永不写、DB 接线无限期搁置且无告警。建议 P3（加 TERM/INT trap 或陈锁 mtime 阈值自愈）。
- 微竞态（P3）：端口 pin 写在锁外——`resolve_service_port` 于 ：116 调用，锁在 ：134 才获取，两个并发首跑 deploy 可同时写 pin 文件（随后仍会在锁上串行，后果有限）。

### F5（P3，文档腐化）：deploy/sql/README.md:88-89 的同步契约声明不实
- 声称 "schemas/baseline/ 与 sql/schema/ 内容完全相同…通过 migrate-sql-files.sh 保持同步"。实测两文件漂移 352 行，而 `deploy/sql/migrate-sql-files.sh` 是一次性搬家脚本（方向 installer embeddata → baseline，:23-25），不在任何 CI/钩子链里。唯一守卫 verify-stats-schema-mirror.sh 只在 build-image.sh 内触发。

### F6（P3，既有行为备注，非本轮回归）：显式端口环境变量在 pin 存在时被静默忽略
- `scripts/deploy-local-sys.sh:64-66`：pin 文件存在即提前 return，`LLM_GATEWAY_SYS_SERVICE_PORT` 显式指定的"已占用则 die"检查（:70）被绕过。属修复前既存语义，仅登记。

## 二、核实为健康的面

**A1 Windows PID CRLF** — 属实且完整。写侧 `deploy-local-sys.sh:394` `[IO.File]::WriteAllText`（无换行无 BOM）；读侧 ：165 `tr -d '[:space:]'` 收敛在 `gateway_pid()` 单一漏斗，start（:375、:403）/stop（:413）/status（:437）三消费路径全过兜底。

**A2 mkdir 原子锁主体** — 属实。LOCK_DIR mkdir 原子竞争、holder pid 落盘、EXIT trap 保留 rc；三动作经 case 精确圈定，只读动作不抢锁。db-attach 独立锁与部署锁目录不同无递归自锁；db-attach 内部调 deploy 撞锁 exit 64 → 自身 EXIT trap 清锁，下次 cron 重试，闭环正确。

**A3 端口 pin 移位** — 属实。定义 :63-80；唯一调用点 :116 在参数解析后；`--install-root` 分支 ：110 重算 PORT_PIN_FILE；写侧唯一入口 ：78-79 被 ：68 `case deploy|start` 守卫——status/logs/verify/stop 零 pin 写副作用。

**A4 strict secrets 门 14 条新增目检** — 全部真·假阳性，无一压住真泄漏（7× i18n password 标签、UsersView 事件绑定、configs/sensitive_patterns.yaml 自指涉 ×2、测试夹具 ×4、tests/unified_probe env 引用）。交叉验证：去 baseline 跑 strict 全树 CRED_ASSIGN 54 处，14 个新 key 全在列（条目 load-bearing 且行号精确）；带 baseline strict 实跑 rc=0。1cc116533 旧条目抽 4 条复核成立。

**A5 sync-to-github strict 调用链** — 确认。Step1 sync-to-github.sh:108 `--mode=strict --tracked-only --baseline=...`；Step4 :138 对镜像 checkout 再跑 strict。双段实跑 rc=0。

**B 补充**：baseline 头部有完整来源注释（2026-08-04 从 252 反向工程 + Regenerate 指引）；dump-schema.sh 真实存在且自带"勿原地覆盖"警示；installer 有 TestStartupFilesAreAllEmbedded 防漏嵌。

## 三、未覆盖项与原因

1. 未端到端跑 deploy/build-image.sh（会构建镜像，违反只读约束）；F1 结论来自直接运行校验器本身。
2. 未对真实 DB 执行 installer fresh-install 验证 566 瞬态窗口（主会话后续以 fresh-install e2e 补齐验证）。
3. 24 条旧 baseline 条目只抽检 4 条逐行目检。
4. auto-route 是否所有消费方都有周期兜底未穷尽。
5. sanitize 族 Go 文件不在本域任务书范围，未复审。
