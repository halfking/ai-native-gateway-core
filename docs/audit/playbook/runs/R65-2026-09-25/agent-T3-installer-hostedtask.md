# T3 installer/hostedtask/secret-scan 子代理报告（窗口：2026-09-24T05:00..2026-09-25T05:00, HEAD 9635b9b17）

> R65 轮只读子代理原文留档。主代理复核结论见轮文档。

## 一、发现（候选，待主代理复核）

| # | 级别候选 | 发现 | 证据 file:line | 建议处置 |
|---|---|---|---|---|
| 1 | P3 | **"解锁 github 推送"声明与实态偏差**：以 sync-to-github.sh 的原样命令在 HEAD 复跑扫描（strict 模式），结果 0 BLOCK（属实）但余 **1161 WARN**（INTERNAL_DOMAIN 1109 + CRED_ASSIGN 52）→ strict 判定 `DECISION="block"`。即 3b410b6ab 仅放行 27 条后，同步脚本 Step 1 的严格闸仍会被千余条 WARN 卡死 | scripts/scan-secrets.sh:490-497；scripts/sync-to-github.sh:100-105；scripts/scan-secrets.baseline:171-201 | 待复核实际推送通道；要么补齐 baseline，要么把声明改为"仅清 BLOCK"【登记】 |
| 2 | P3 | **1c0c200be 修复#1 的回归面未补**：deploy/prometheus DSN 参数化为 `${PG_USER}:${PG_PASSWORD}@...`，但 .env.example 不含这 6 个新变量 → 按模板 up 起的服务容器拿到 `postgres://:@...` 必然连库失败。属 legacy Phase-0 dev 资产 | deploy/prometheus/docker-compose.yml:133；deploy/prometheus/.env.example:16-23 | 补 6 变量【已修】 |
| 3 | P3（**前置存在**，非本窗口引入） | **回调投递在途回写 vs 终态再入队竞态可丢召回回调**：`RecordCallbackOutcome` 回写 `WHERE task_id=$1` 无 event_id/status 守卫；`RecallTask` 终态路径每次召回把台账重置 pending（新 event_id）。若 deliverer 正投递 E1 期间 recall#2 重置为 E2，E1 投递成功回写会把 E2 标为 delivered → E2 永不投递。本窗口 cancel 入队路径**无**此问题（cancel 要求非终态，与在途投递互斥） | domains/hostedtask/store.go:883-891、:464-471、:825-861 | 回写加 `AND event_id=$2` 守卫；挂账 callbacks 闭环后续轮【登记】 |
| 4 | P3（形态记录） | 1c0c200be 的 #2/#3/#4/#5/#6 采用"让扫描器看不见"的规避形态（printf 拆 DSN、`PRIVATE KEY`→`[REDACTED-KEY]`、sed 正则拆段）而非 baseline 登记。逐处亲验：内容**全部为占位符/无真实凭据**，拆段正则组合后与原式逐字符等价。无真实凭据残留或转移（P0 排除） | docs/handoff/2026-09-19-*.md:127 等；scripts/deploy-local-lib.sh:798-824 | 记录为先例风险：真凭据严禁走此形态，必须轮换+删除【登记】 |

## 二、核实为健康的面

- **hostedtask cancel 终态回调闭环（69f5aa6a1）**：恰好一次入队成立。CancelTask/RecallTask/SettleTask 全部 `SELECT…FOR UPDATE` 行锁内 check-then-act；终态 sticky + `won=false` 提前返回保证二次取消不走 cancelRowInTx；事件与回调置 pending 同事务原子，入队失败=整事务回滚；投递体形状亲验一致；状态机 CHECK 与迁移 711+742 一致（recalled 已入白名单）。
- **自动激活契约（0f57fb0a5）**：register 即激活与主控真实代码吻合；device_code=GW-+sha256 前 12 hex 非凭据；instance_token/refresh.token 仅落 state/*.token（0600 原子写）；activation.json 0600 且 launcher /status 白名单无 token/license；license key 不进日志。**refresh.token 仍零消费方**——R64 休眠判定仍成立。
- **launcher UI 轮询 vs activation 写入竞态**：写侧 tmp+rename 原子，读侧每请求重读，JSON 损坏降级不崩。健康。
- **3b410b6ab 放行形态**：键为精确 `file:line:CATEGORY`，仅 INTERNAL_DOMAIN 27 条，逐条抽验均为公开域名；**无搭车放行**；线号漂移只会使放行失效（fail-closed）。
- **installer 窗口回归面**：embeddata 已同步 745/746（含 down）与 800；stats_migrations_test 已注册；heartbeat 读 token 收敛单实现；installer-ci.yml 无 secrets。
- **机器验证**：go vet（hostedtask+bg+installer 4 包）全过；hostedtask/activation/instancemeta 单测全绿；activation 19 测试覆盖关键钉桩。

## 三、未覆盖项与原因

1. 实际 github 推送是否成功——仓库外事实（发现#1 标"待复核"）。
2. TestStoreAgainstPostgres 真库路径——禁连库未复跑。
3. 主控端 register/heartbeat 业务语义全量——超窗口范围。
4. launcher plan/prepare/apply 升级流与 daemon token 管理——非窗口改动面。
