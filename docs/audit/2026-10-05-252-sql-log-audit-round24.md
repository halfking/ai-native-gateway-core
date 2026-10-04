# 252 PG SQL 日志审计第二十四轮（2026-10-05）—— 传输层 1GB 截断致 19.9h 证据缺口 + modality 证据列 []byte hex 新病根修（迁移 825 worker，部署三实例）+ R23 五项终验

以 round23 §八 + round22 §八为起点（起点重算：本轮开局本地 main=54854ea00=origin/main，无分叉；工作区带并行线 10-04 245 部署（2459-0978171a）的未提交版本身份五文件，全程原样保留、未替并行线提交）。**取证窗 = 2026-10-04 06:02 → 10-05 06:16（名义 24h），但其中 10-04 06:02 → 10-05 01:54（19.9h）被 podman 1GB 日志上限静默截断丢失**（§零.1）；**实际可用日志窗 = 10-05 01:54:39 → 06:16:13（4h22m）**，缺口以 DB 侧状态桥接（capability 表水位、822 idx_scan、pg_stat_statements 累计、schema_migrations 台账）。快照 06:16。本轮由用户 /goal 指令触发（每日 06:00 automation-f0a3bf63 今晨是否触发未核对——快照时间在此前之后均成立）。

---

## §零、传输层巡检：podman 1GB 上限静默截断（新）+ logrotate 三项异常并案

1. **【本轮新发现·数据丢失机制闭合】podman k8s-file `log_size_max=1GB` 到顶静默丢弃**：`podman inspect pg-252-pg17` HostConfig.LogConfig 尾参 = 1GB。R23 快照时（10-04 06:02）ctr.log 表观 972MB；10-05 01:54:37 起文件从零重写（现行首行时间戳），无归档、无轮转记录、logrotate 状态未动（仍是 10-04 03:15:03）——conmon 侧到顶截断不产生任何可观测痕迹。19.9h 生产 SQL 日志就此灭失。**该上限此前从未被登记**：R15 时代结论「json-file 无上限+logrotate copytruncate」已过时（json-file 驱动 = k8s-file 前身，上限在容器级 HostConfig 上）。
2. logrotate 现行配置（`/etc/logrotate.d/podman-pg-252`，mtime 07-29 未动过）：`daily rotate 3 maxsize 100M compress copytruncate`（dateext 疑为 /etc/logrotate.conf 全局）。但 10-05 03:37 cron 确实跑了 logrotate（/var/log/cron start+finished 各一条，3s 完成）却**没有轮转本文件**（状态仍 10-4 3:15:3、无 ctr.log-20261005 产生）——daily 到期却未轮转的机制未明，移交运维轨。01:54 截断后文件仅 4MB，maxsize 100M 条件也未到。
3. `ctr.log-20261004`（963,012,664B 明文，R23 已消费其内容）仍在原位未压缩——「compress 配置在、归档不压缩」反常（R23 §零）持续第 2 天。运维轨登记维持。
4. 磁盘 93%→50%（95G 可用）发生于 10-04 白天（18:01 disk-watch 已报 48%）——并行运维轨的大清理（同日 /etc/cron.d/pg17 23:49 更新、新增 ursm-snapshot-health :17 / ursm-snapshot-payload-bloat :23 / pg-table-bloat-check 05:07 三道巡检 + pg17.bak.20261004-234856 备份）。本轮全程未触碰其工作面。
5. 观察：pg17-disk-watch.sh 每 tick 在 /var/log/cron 出现 4 条同秒 CROND 行、emergency-cleanup 日志同秒 4 条 invoked——root crontab 无重复登记、/etc/cron.d 仅 pg17 一份（.bak 带点不加载）；来源未明（只读采样，无害），移交运维轨。

---

## §一、错误家族全景（可用窗 4.36h；头锚定 `CST [pid] ERROR:` 二段计数；×7为我方负例对照产物，剔除后计 48）

| # | 家族 | n（4.36h） | 折率 | 归因 | 处置 |
|---|---|---|---|---|---|
| 1 | **`invalid input syntax for type json`（`\x` hex，char 429/430/437）** | **6** | 1.4/h | **新病：`bg/modality_verification.go` persistRow $10/$11 裸 `[]byte`（迁移 825 worker，10-04 14:05 b992c01b3 引入；245 预生产 19:05 起 0978171a 带病运行即本源）** | **本轮根修+部署**（§四.1），06:43:58 首行落库后归零 |
| 2 | `canceling statement due to statement timeout` | 41 | 9.4/h | probe cycle 目标查询已知族（R8 老病） | 维持 |
| 3 | `connection to client lost` | 37 | 8.5/h，02h 26 条集中 | **dev 隧道抖动**：journal 01:53 mihomo `dial pg-252-pg17:5432 → R1.tube-cat.com:9115 i/o timeout` 群发——远端开发会话经代理连 252 PG 的连接成批掉线；与 acc 风暴无关（风暴 10-03 21:21 已止） | 外部会话自噪音，登记 |
| 4 | `canceling statement due to user request` | 29 | 6.7/h | 应用侧 context 取消已知容忍族 | 维持 |
| 5 | `could not determine data type of parameter $2` | 7 | 1.6/h | **外部股票筛选器**（`FROM fundamentals f JOIN stock_basic s … WHERE s.industry=$1 AND ($2 IS NULL OR l.code<>$3)`，Extended Protocol 裸 NULL 参数不可判型；非本仓——网关全量 SimpleProtocol 不产生此形态） | 外部催办（与 #7 同客户端并案） |
| 6 | `relation "llm_usage_stats"/"llm_optimization_*" does not exist` + `column cl.task_type` | 5 | 04:50:49 同秒突发 | 同一外部股票/分析客户端对其预期 schema 的探测（表不存在于本库） | 随 #5 并案 |
| 7 | `integer out of range` | 3 | — | 同客户端 `INSERT INTO limit_up_temp`（涨停温度表）溢出 | 随 #5 并案 |
| 8 | `function pg_size_pretty(double precision)` / `column "session_id"` | 2 | 03:42 | 并行审计会话侦察自噪音（R23 同款） | 剔除 |
| 9 | `division by zero` | 1 | 02:38 | 并行会话手写对账查询 `round(100.0*count/count)` 未 NULLIF（session_turns 2h 覆盖率） | 剔除 |
| 10 | startup packet ×2 / frontend protocol ×2 | 4 | — | 端口扫描/探活噪音 | 剔除 |
| 11 | `inconsistent types deduced for parameter $4`（E6 pocket） | 53 | **12.2/h** | E6 opencode-pocket，24h 恒 12/h 节奏纹丝不动 | 催办维持 |
| 12 | 我方负例对照 `…at character 374` | 1 | 06:35:53 | 本轮 §四.1 验证在真表跑 hex 负例所产（`r24-probe-model` 语句原文可证） | 剔除（自噪音） |

二段计数：本库真阳性 = #1（已修）+ #2/#4 本库子集 + #11（外部库 pocket 但持续在催办清单）；acc_interval（timestamptz<interval）**0 条**——R22 的 acc 风暴连续第 3 天零复发，销案维持。

## §二、慢 SQL 全景（≥1s 共 629 条 / 2,584.6s / 4.36h ≈ 593 s/h；R23 全天口径 3,596 s/h 不可直比——本窗为夜间+膨胀轮收工后）

形状级统计（前 16，纪律 56 配对法 + 纪律 57 同行提取两级分离；提取器经三轮自纠，见 §六.61）：

| 形状 | n | sum | max | 判定 |
|---|---|---|---|---|
| analyze_llmateway_table_stats('2') | 11 | 385.6s | 58.1s | D7' 已知（巨 payload 行宽采样），未破 10min 线 |
| `SELECT t.id…FROM session_turns t JOIN request_logs` | 24 | 340.5s | 27.3s | 会话镜像/完整性探针族（G2 谱系），夜间 ~5.5/h，量级与 R23 同族相当，登记不立项 |
| 路由刷新读臂（latest_bucket SELECT） | 76 | 163.6s | 18.4s | P2 维持：夜间均值 2.15s ≈ 白天 2.6s，**夜间照旧慢**（tick 驱动非负载驱动），7d 回看提案证据再加一条 |
| project_backfill（WITH targets） | 26 | 158.2s | 24.6s | R8 已知 |
| probe_cycle 目标查询 | 34 | 124.3s | 6.7s | R8 已知（夜间均值 3.7s，白天 3.9s 持平） |
| ursm 快照聚合（date_trunc hour） | 4 | 117.2s | 57.6s | **02:18 两条 ~57.5s（830 应用前）+ 05:17 两条 1.1/1.3s（830 应用后）**——并行线 830 分区化初见成效；验证归其轨道 |
| `SELECT c.id FROM credentials c -- v1 臂改为 LEFT JOIN` | 49 | 106.2s | 4.7s | 路由 v1 臂候选查询，夜间 11/h、avg 2.2s——归属路由 P2 家族（与读臂同病：credentials/last_error 过滤无覆盖索引），并档 |
| `SELECT cmb.credential_id, pm.raw_model_name…`（probe 目标变体） | 18 | 97.2s | 11.4s | probe_target 同族 |
| `SELECT (SELECT count(*) FROM session_turns…)` 覆盖率 | 26 | 87.4s | 13.7s | 会话审计探针族，同 row-2 并档 |
| `WITH latest AS (…usage_facts JOIN stats_event_dedup)` | 9 | 80.7s | 17.2s | usage 消费方查询，量级小，观察 |
| `WITH used AS…`（路由 used 查询） | 16 | 71.9s | 19.6s | 已知 cancel 伴生族 |
| 一次性 pg_total_relation_size 普查 ×1 71.2s + ×9 80.7s | 10 | 151.9s | — | 运维轨/膨胀轨侦察自噪音（04:40 周日巡检与夜间手工），剔除 |
| REFRESH MATERIALIZED VIEW CONCURRENTLY routing_analytics_7d | 25 | 59.1s | 4.5s | R12-F1 后夜间常态（avg 2.4s） |
| `WITH matched AS MATERIALIZED（mo.*/mc.context_window…）` | 24 | 55.8s | 9.8s | 模型匹配查询（路由/抽屉谱系），夜间 5.5/h，登记 |
| `WITH win AS (credential_id COUNT(*) FILTER…)` | 23 | 55.3s | 7.0s | 凭据 win 统计（cancel 伴生），登记 |
| **路由刷新写臂 INSERT credential_model_index_hot** | 28 | 53.0s | 5.7s | P2 证据：夜间每小时都有（01-05h 分布 1/9/12/2/4），avg 1.9s（白天 2.89s）——三臂（读/写/删）全天候在费 |
| session_health_worker sweep | 1 | 1.5s | 1.5s | **822 生效终验**：原 25.8-29.4s → 1.5s |
| cmb_update（revert/mnf_cooling） | 1 | 1.2s | 1.2s | **R23 预言证实**：膨胀轮收工后夜间归零（仅 1 条贴线） |
| REINDEX / VACUUM | 2 | ~6s | 3.1s | 膨胀轮已收工（10-04 18:19），残量贴线 |

**pg_stat_statements 交叉证实（累计自 09-29 D6 重建，6 天窗，非本窗）**：总耗时榜前列 = `pg_advisory_xact_lock(session_turns_advisory_lock_key)` 301,854s/3.03M 次/均值 100ms（写臂锁等待，绝对值大但为等待时占比）、路由读臂 133,987s/718ms + 写臂 93,967s/503ms + **DELETE FROM credential_model_index_hot 68,775s/620ms（第三臂，R23 未单列）**——三臂合计 ~297ks/6 天，7d 回看提案收益继续加重；REFRESH routing_analytics_7d 122,399s/均值 19.9s（含 D12 前时代，不可直比本窗）。

---

## §三、R23 遗留项逐项复核（§八①-⑥）

| R23 登记 | 本轮复核结果 |
|---|---|
| ① capability 部署终验 | **PASS**：credential_model_capabilities 97→**138 行**持续增长（种子满水位 612）；max(last_tested_at)=**10-05 05:12:38**（跨丢失窗口持续写入）；json_hex-capability 自 10-04 06:40:13 后连续第 2 天零复发 |
| ② 822 部署后 sweep 归零 + 台账水位 | **PASS**：idx_session_summaries_health_pending **idx_scan=70**（≈1 次/h/实例节拍）、EXPLAIN **Parallel Seq Scan→Index Scan**、窗内 sweep 仅 1×1.5s（原 25.8-29.4s）；台账 822→**830**（并行线 18:36 推 823-826、今晨 03:05 推 830），链健康 |
| ③ 路由 7d 提案拍板跟踪 | P2 维持待拍板；**证据三连加重**：读臂夜间照旧 + 写臂全夜在费 + pss 第三臂 DELETE 68,775s 浮出；11-02 day-2 DROP 自然缓解节点不变 |
| ④ cmb_update 随膨胀轮收工消失 | **证实**：夜间仅 1 条 1.2s 贴线（R23 白天 ×763/14.8s） |
| ⑤ ursm 快照聚合夜间慢查归因 | 时序证据：830（03:05 应用）前 57.5s×2 → 后 1.1-1.3s×2，并行线分区化有效（归其轨道验证，本轮不立项） |
| ⑥ E6 催办维持 | 53 条/4.36h = **12.2/h 恒定**，节奏不变，催办维持 |

另：R23 §五.5 vacuum-bloat「服务器侧坏版本仍在位」——**本轮收口**（§四.2）。

## §四、本轮修复

### §四.1 modality 证据列 []byte hex——同族第四/第五断点根修 + 三实例部署

- **病灶**：迁移 825/826（b992c01b3，10-04 14:05，多模态能力分级）的 `bg/modality_verification.go` 两处裸传 `json.Marshal` 产物：persistRow $10/$11（carry_evidence/read_evidence → model_modality_verification）、rollupVerdict $2（modality_evidence → models_canonical）。SimpleProtocol 把 []byte 内联为 `'\x7b22…'` bytea hex 字面量，jsonb 解析必炸——与 R11 FIX-C（providerprofile）、R22（capability evidence_json）**同族第三实例**（本轮起该家族共 5 个断点位）。失败后果：model_modality_verification 表 **0 行**（探测全白跑）、判级与 canonical 回写全灭、且静默（仅 PG 日志可见）。
- **归因链（纪律 58 修正过程见 §七.A3）**：6 条错误最终归于 245 预生产（19:05 起运行 0978171a ⊇ b992c01b3，真钥真凭据全功能跑）；154 的 2454-a926410f 同样含病灶（部署前夜时点未及取证，无窗口对应错误——其 tick 相位未落在可用窗）；252-dev（5461c1676）不含 worker。
- **修复**（提交 8ab8a4fd6，rebase 于并行线 e974a0d79 之上）：两处调用点复用同包 `capabilityEvidenceParam`（R22 既有工具）；`TestModalityRowEvidenceParamGuard` AST 守卫钉死三个参数名不许裸传；`TestModalityEvidenceParamRealDB` SimpleProtocol 池正反对照（镜像 R22 范式）。`go build ./...` 干净、bg 全量单测 30s 绿。
- **252 生产表正反对照**（事务内、零残留）：hex 形式逐字复现生产错误（`Token "\" is invalid`）；string 形式 `INSERT 0 1` 成功。
- **部署**（245→154 晋级门禁，纪律 59 全程 union 后部署）：245 old=2459-0978171a → **2460-8ab8a4fd**（总 153s/切换 15s，解密冒烟 providers=18/creds=16/failed=0）→ 154 old=2454-a926410f → **2461-8ab8a4fd**（总 115s/切换 14s，冒烟同上全绿）→ 252-dev VERIFY_COMMIT=8ab8a4fd6 pass。三实例构建谱系统一。
- **部署后判据**：**06:43:58 model_modality_verification 首行落库**（新 245 二进制启动首轮 tick），此后 json_hex-modality **零复发**（06:35:53 的 char-374 一条为本轮负例对照自产物，语句原文可证，已剔除）。

### §四.2 vacuum-bloat 服务器侧坏版本——两次移交后本轮收口

仓库模板 scripts/252-monitor/pg17-vacuum-bloat.sh 早于 10-03 已修（SET 与 VACUUM FULL 同 `-c` 被包进隐式事务块 → PGOPTIONS 方案），但服务器 /opt/scripts 一直跑旧版（每个周日 03:15 100% 失败，R22 预言、R23 再证）。本轮：容器内先验证 PGOPTIONS+VACUUM FULL 模式可行（temp 表实测）→ 备份旧版（.bak-r24）→ 模板同步上服务器（bash -n 过）→ **终验节点 10-11（周日）03:15**。

## §五、登记与移交清单

1. **[P1·已闭环]** modality 证据列修复部署完成（§四.1）。R25 首查：modality 表行数增长（02:18 相位对齐后应每 tick 增量）、models_canonical 出现 semantic 回写、json_hex-modality 连续零复发。
2. **[运维轨·新]** ① podman 1GB log_size_max 静默截断（§零.1）——要么调大并配合轮转、要么接受每天 ~20h 一轮的日志灭失（本轮已实际损失 19.9h）；② logrotate daily 到期未轮转（§零.2）；③ 963MB 明文归档压不下去（§零.3，第 2 天）；④ disk-watch 4× 重复 CROND 来源（§零.5）。
3. **[外部·催办]** 股票筛选客户端三签名并案（$2 不可判型 ×7 / limit_up_temp 溢出 ×3 / 预期表不存在 ×4）——同一 mihomo 隧道流量，末次 06:15:40；E6 pocket 12/h 维持。
4. **[P2·提案待拍板·三臂证据齐]** 路由候选 7d 回看（读 134ks+写 94ks+删 69ks 累计 6 天）；11-02 day-2 DROP 自然缓解节点不变。
5. **[测试债·新]** bg 真库用例批量跑存在顺序污染：`-run RealDB` 全量时 TestMigration762ProjectBackfillChain_RealDB 失败（前面 TestLedgerReconciler_RunOnce_RealDB 真库写入所扰），单独跑即绿——真库回归需互相隔离或固定顺序（R22 §五.6 ledger 数据态敏感同族）。
6. **[观察]** `SELECT t.id…session_turns JOIN request_logs` 夜间 340.5s/24 条 + 覆盖率查询 87.4s/26 条——会话镜像/审计探针族，量级稳定，若增长再立项。

## §六、新纪律候选

- **60**：实例日志取证先验三件事——ctr.log **首行时间戳**（截断痕迹）、podman `HostConfig.LogConfig` 尾参（size 上限）、logrotate 状态文件；「文件比预期小」本身就是事件，不是安静。1GB 上限类截断无任何服务端痕迹，只能靠时间戳断崖发现。
- **61**：`git stash push <pathspec>` 失败（未跟踪文件不匹配）后**严禁**以 `;` 链接 `stash pop`——pop 会弹错stash（本轮弹出了 10-01 的 mavis 旧 stash 污染 6 文件）。pop 前必须核对 `git stash list` 栈顶身份；恢复用外科手术式 `git checkout HEAD -- <被触碰文件>`，全程以 status 快照对账。
- **61.a（提取器自纠）**：stderr 日志解析器的「同行语句」必须剥 `statement: ` 前缀再走 startswith 分类，否则全部单行语句落 misc；raw byte string 里 `rb"\\s"` 是字面反斜杠+s（双重转义），正则写完必须对已知形状自测——本轮 misc 桶 430 条虚胖与首次补丁无效都是它。

## §七、自审计

- A1：本方负例对照（06:35:53）先被计入 json_hex 复发（7 条），靠 char 位置（374 vs 429/437）异常倒查归为自产物——**自产噪音必须先于结论剔除**，否则会把「修复后归零」写成「仍在复发」。
- A2：modality 归因第一版（「无任何已部署二进制含 worker」）**错了**：只核对了 25a864398/5461c1676 两个我已知的部署，漏了并行线 19:05 对 245 的 0978171a 部署。修正触发器是 245 部署回显的 `old=2459-0978171a`。纪律 58 补强：实例归因必须枚举**全部**历史部署（含并行轨的），不是枚举我记得的部署。
- A3：stash 事故（§六.61）全程如实记录：污染 6 文件（5 冲突+1 静默合并）全部还原 HEAD，mavis stash 完璧归赵（stash list 复核），我的三文件改动与并行线五文件部署记录零损伤（status 对账）。事故根因是失败链路用 `;` 续接破坏性命令。
- A4：19.9h 证据缺口如实披露并以 DB 桥接补强，未用 4.4h 窗口计数冒充 24h 口径；所有折率标注窗口基数。
- A5：并行线工作面零触碰：URSM 巡检 cron、830 分区化、bodies 读方迁移、工作区五文件部署记录原样保留；其 830 的 ursm 收益只做时序旁证、验证归其轨道。

## §八、下一轮提示词（建议）

> 以本文 + R23 §八为起点。优先级：① modality 修复终验（表行数增长、models_canonical semantic 回写、json_hex 连续零复发；R24 部署后判据 06:43:58 首行已落）；② podman 1GB 上限处置跟进（运维轨拍板调大/换驱动/接受灭失，窗口取证前先验截断）；③ 路由 7d 提案拍板跟踪（三臂证据在手，11-02 自然缓解）；④ E6 与股票客户端外部催办维持；⑤ 10-11 03:15 vacuum-bloat 同步后首跑终验；⑥ 会话镜像探针族（340s/夜）若增长则立项。纪律沿用 ⑪-59 + 60/61/61.a。

## §九、产物与物证

- 代码：提交 8ab8a4fd6（bg/modality_verification.go 两处 + AST 守卫 + realdb 回归，三文件 +109/-2）；推送 origin/main=8ab8a4fd6（rebase 于并行线 e974a0d79）。
- 部署：245=2460-8ab8a4fd（old=2459-0978171a）、154=2461-8ab8a4fd（old=2454-a926410f）、252-dev=8ab8a4fd；门禁解密冒烟两次 16 creds failed=0。
- 运维：/opt/scripts/pg17-vacuum-bloat.sh 已同步仓库模板（备份 .bak-r24）；252:/tmp/pg252-r24/（ctr-snap-r24.log 4.1MB、errors_r24.tsv、slow_r24.tsv 629 行、summary_r24.json、r24_extract.py 三轮自纠终版、r24_bridge.sql）。
- 关键实测值：可用窗 01:54:39→06:16:13/4.36h；错误 48 条真阳性（剔自噪音 8+外部 16）；慢查 629/2,584.6s；capability 138 行/05:12:38；idx822 idx_scan=70+Index Scan；modality 表 0 行→06:43:58 首行；台账 830；磁盘 50%/95G 可用。
- 本报告：docs/audit/2026-10-05-252-sql-log-audit-round24.md。
