# Handoff — 余额下限守卫(balance-floor guard,migration 701)

- 日期:2026-09-13
- 提交:1a89c32fe(merge 67fce6914 已推 origin/main)
- 状态:已实现 + 自审修正 + 测试全绿 + 已推送

## 需求与结论

需求(/goal):自动感知每个供应商凭据的余额;保留最后 50W token 的下限,到达即从路由池摘出(不是停用),充值/重置后自动回池;重点是 glm、minimax 原厂。

结论:可行。网关原有基础设施只支持"货币余额"型探测(openai/deepseek/siliconflow,且唯一写入点 cycleAll 在默认新探测模式下不运行);GLM/MiniMax 没有公开现金余额 API,只有订阅套餐接口。本次按两种语义落地:

1. 套餐额度(zhipu/minimax):探测 + 落库展示 + token/百分比下限摘除。
2. 货币余额:guard 自刷新 balance_usd(15 分钟新鲜度),货币下限摘除。

## 实测钉实的厂商接口(2026-08,cc-switch/TokenScope 交叉验证)

- zhipu:`GET {origin}/api/monitor/usage/quota/limit`,Authorization **不加 Bearer**;`limits[].unit` 3=5h/6=7d 窗;`percentage` 方向=**已用**;`remaining` 绝对量(TOKENS_LIMIT 单位=token);HTTP 200 仍可能 `success=false`。
- minimax:`GET {origin}/v1/api/openplatform/coding_plan/remains`(fallback `/v1/token_plan/remains`),Bearer;字段方向=**剩余**需 100-x 反转;周窗仅 `current_weekly_status==1`;业务错误在 `base_resp.status_code`;只有百分比无绝对量。
- 两者 URL 都必须从 base_url 的 **origin 重建**(base 带 /api/paas/v4、/v1 前缀)。

## 关键行为(改动文件)

- `sql/migrations/startup/701_credential_balance_floor.sql` + `db/db.go ensureCredentialBalanceFloor` + 3×01-schema baseline:credentials 新增 `balance_floor_usd/quota_floor_tokens/quota_floor_percent`(NULL=不启用,admin PATCH 0=清除)+ `plan_quota_kind/windows/remaining_tokens/used_percent/checked_at`。
- `bg/balance_floor_guard.go`(新):5 分钟 sweep(`LLM_GATEWAY_BALANCE_FLOOR_INTERVAL`,`LLM_GATEWAY_BALANCE_FLOOR_GUARD=off` 关闭)。摘出=写 `quota_state='balance_exhausted'`+`availability_state='suspended'`+`state_reason_code='balance_floor'`(候选 SQL 与 v_routable 视图天然排除→即刻出池,`trg_notify_auto_route_refresh` 广播缓存失效);恢复=滞回带(floor×1.1 / floor−2pp / used==0)+ 所有权守卫。**绝不写 manual_disabled**。persistPlanState 不碰 state_updated_at(保 AutoRevoker 窗口真实)与 balance_last_checked_at。
- 所有权不变量(audit P0/P1 修复,跨全部 quota_state 写入方):BalanceQuotaProbe 定期选点、webhook OnQuotaRecharged、node_probe_write_through(每请求成功路径!P0:否则高峰凭据被 in-flight 成功瞬间 un-pull 乒乓)、writer.go 配额分支——都豁免/不重打 `balance_floor`。摘出 WHERE 守卫 manual_disabled+quota_state='ok'+availability 不属于 auth_failed/他方 suspended。
- `internal/providercap/capability.go`:抽取共享 `FetchBalanceUSD/ExtractJSONPath`;`credential_probe_v2.probeBalance` 委托(行为不变)。
- `admin/provider_credential.go`:PATCH/列表暴露下限与套餐额度字段(向后兼容)。
- `cmd/gateway/main.go`:装配 `balanceFloorGuard`(NewBalanceFloorGuard+SetKeyring+Start,Stop 先于 BalanceQuotaProbe)。

## 测试命令与结果

```
go build ./...                                                    # OK
go vet ./bg/ ./admin/ ./db/ ./domains/credential/ ./internal/providercap/ ./cmd/gateway/   # clean
go test ./bg/ -count=1                                            # ok 4.2s
go test ./admin/ -count=1                                         # ok 67s
go test ./db/ ./internal/providercap/ ./sql/migrations/startup/ -count=1   # ok
go test ./domains/credential/ -count=1                            # ok 14.7s
```

新测试:bg/balance_floor_guard_test.go(zhipu/minimax 解析器含方向/业务错误/字符串数字/周窗激活矩阵、evaluatePlanFloor 滞回+极小 floor 卡死边界、originQuotaURL、flexNum、写路径所有权契约 TestBalanceFloorGuardOwnershipExemptions、main.go 装配契约)+ balance_quota_probe_test.go 豁免契约。

## 使用方式

给凭据配下限(PATCH /api/providers/{id}/credentials/{cid}):
- token 包型:`{"quota_floor_tokens": 500000}`(zhipu TOKENS_LIMIT,字面"留 50 万 token")
- 订阅窗口型:`{"quota_floor_percent": 95}`(minimax 唯一选择;zhipu credit 套餐也用它)
- 货币型:`{"balance_floor_usd": 1.0}`
- 摘出后凭据在 admin 显示 balance_exhausted/balance_floor reason;充值或窗口重置后 1-2 个 sweep 内自动回池;清下限=恢复常驻。

## 遗留风险 / 已知限制

1. minimax `/v1/token_plan/remains` 响应形状按 cc-switch 参考"同 coding_plan 形状"处理,未实测(无订阅 key);失败即 fail-open 跳过。
2. zhipu `CREDIT_LIMIT` 的 `remaining` 单位是积分不是 token,token 下限只认 TOKENS_LIMIT——credit 套餐请配百分比下限(admin 未做厂商侧强校验,靠文档约定)。
3. 摘出后 admin reset-state/force-probe 可人工越过,但下一 sweep(≤5min)会按 floor 重新摘出;要常驻需清下限(已在注释文档化)。
4. web UI 未加新字段的表单控件(API-first,JSON 字段已可用);`provider/client.go` 候选 SQL 与视图对 `balance_exhausted` 的排除是既有行为,未改动。
5. 套餐探测串行、单 sweep 上限 200 凭据 + 3 分钟 ctx;>200 家 zhipu/minimax 大规模场景需再评估(有 ORDER BY plan_quota_checked_at 公平轮转)。
6. 本地/252 库尚未部署验证运行时行为(仅单测/构建);下次 deploy-local 或 pg-schema-sync-252 时 migration 701 会随 ensure 生效。

## 当前验证结论

> 对 1a89c32fe(balance-floor guard)的部署级验证目前只能分层表述：已证 migration 701、zhipu 真实凭据摘出/恢复闭环、web 构建与相关单测；currency pass A/B/C 仅为 mock 验证；未证 minimax `/v1/token_plan/remains` 真实响应、当前 Docker/:8782 容器身份与 `go version -m`、以及登录后的 live 404 探针。详见 `docs/changelogs/2026-09-13-balance-floor-guard-deploy-verify-and-fixes.md`。已落地的代码修复为清下限自动回池(`releaseClearedFloorCredentials`,66a9f8e6a)、planTypes 下拉值域对齐(77956aeb5)、以及惰性 plan 下限边界修正(870fac658:非套餐厂商清货币下限后残留 plan floor 不再卡死,顺带复审 probe-recovery closeout 并行线与所有权不变量交互全部健在)。下一轮不得将 mock 或静态证据写成完整生产闭环。

## 下一轮提示词(建议)

> 先执行 `git fetch origin main` 并记录新的 `origin/main` SHA、HEAD 差异和工作树 WIP；勿覆盖 VERSION/version.json/web/public/* 等并行部署簿记。随后按证据顺序复验：恢复 Docker Desktop 并确认 daemon 健康，`docker ps` 核对 :8782 active/:8781 candidate、tag 与端口，`docker cp` 容器二进制后用 `go version -m` 核对 vcs.revision，再执行登录后的 live 404 探针。最后使用脱敏的真实 Minimax 订阅 key 验证 `/v1/token_plan/remains` 响应字段与 parse；currency A/B/C 若仍为 mock 必须继续标注 mock。任一环境不可用时记录 blocker 与恢复条件，不得宣称部署闭环。

---

# 2026-09-16 轮次 — 九项审计修复落地 + 集成验证(提交 ae41328c4 / 74240e999 / 二轮修正 f915808dc / 三轮修正)

- 状态:已推送 origin/main;单测 -race 多轮全绿;集成验证(真实 PG + mock 厂商控制面)4/4 通过
- 二轮自审修正(f915808dc):F-1 getJSON 请求构造移出重试循环(构造错误此前会绕过 retryableHTTPErr 分类被当传输错误白烧 2 次重试 +1.5s);F-2 probed=0 的空 sweep 不再输出汇总日志(无套餐厂商部署上每 5 分钟一条空行是纯噪音)
- **三轮自审修正:F-3 A-C1 修复不完整** —— 货币 refresh 路径 refreshBalance 直接读 g.keyring 未持读锁(首轮只修了 decryptKey 一条路径);已补 RLock 快照,并把 SetKeyring 竞争测试扩展到同时竞争两条读路径(计划路径用 v1 信封密文、货币路径用 openai catalog 触达解密步,解密失败即返回无网络调用)。教训:**宣称"修复了 X"前必须穷举 X 的全部触发点** —— A-C1 的正确表述是"keyring 的全部读取点持锁",而非"decryptKey 持锁"。

## 修复内容(对应审计编号)

| 编号 | 修复 |
|---|---|
| A-C2 | Start() 重入守卫(lifecycleMu+started),Stop 后不复活 |
| A-C1 | keyring 全部读取点持锁:SetKeyring 写锁 / decryptKey 读锁 / refreshBalance 读锁(三轮 F-3 补齐,keyring RWMutex) |
| B-E1 | 逃生门默认 24h→**2h**(LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS 语义不变,0=关闭) |
| D-L1 | Stop() join workerDone,上限 10s——sweep ctx 派生自 main 的 Background,无界 join 会拖住进程下线 |
| E-B1 | 套餐探测串行→有界 worker pool(semaphore;**errgroup 未进 vendor**,modules.txt 只收 semaphore/singleflight,语义等价) |
| F-L1 | 周期汇总日志 `plan sweep completed`(probed/success/failed/pulled/restored/duration;probed=0 不输出) |
| F-L2 | 探测失败 Debug→Warn,warnGate 每凭据 15 分钟限 1 条(与 #12a 退避同窗),其余降级 Debug |
| G-O1 | getJSON 重试 1+2 次,仅网络错误与 5xx(类型化 httpStatusError),退避 0.5s/1s;请求只构造一次 |
| E-B2 | 货币批次大小可配置——**未做**(计划内 P3 后续迭代) |

新 env:`LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY`(默认 10,钳 1..100)。文档同步:guard 文件头 env knobs、CHANGELOG Unreleased、db-changelog 2026-09-16 条目(更正 R28 记录的 24h)。

## 测试命令与结果(2026-09-16)

```
go build ./... && go vet ./bg/                                    # clean
go test ./bg/ -count=1 -race                                      # 8 轮连续 ok(首轮一次未复现偶发失败)
GUARD_IT_DSN='...llm_guard_it' go test -tags integration ./bg/ -run TestITBalanceFloor -v -count=1
                                                                  # 4/4 PASS
```

集成验证要点(bg/balance_floor_guard_integration_test.go,integration tag,GUARD_IT_DSN 未设置自动 skip):
- 性能:200 凭据×100ms mock 延迟 —— 并发(10)=**2.09s** vs 同构建串行(1)=33.1s,**15.8x**;串行基线覆盖 HTTP+解密+落库全程
- 故障:500 恰 3 次 HTTP 尝试(1+2);Warn 恰 1 条,第二周期限流到 Debug;退避戳落库且绝不写 plan_quota_checked_at;fail-open 保持 ok
- 逃生门:3h 陈旧证据(>2h)释放;健康复核回 ok 且 checked_at 刷新;故障端点保持释放态
- Stop:34µs join worker、幂等、无 goroutine 泄漏
- 汇总日志逐字段断言(probed=200 success=200 failed=0 pulled=0 restored=0)

## 遗留风险(2026-09-16 增量)

1. minimax 双端点 fallback(coding→token)各自独立重试:最坏 2×(3×10s 超时+1.5s)≈63s/凭据,厂商黑洞式故障时单 sweep 会顶到 3 分钟 cctx 上限(fail-open 兜底,#12a 15 分钟退避限制后续节奏);如需收敛可给 fallback 路径单独设尝试预算。
2. 并发 10 对厂商控制面 API 的限流敏感性未在生产实测(计划风险评估项);如观察 429 升温,调低 LLM_GATEWAY_FLOOR_PLAN_CONCURRENCY 即可。
3. Stop() 在在途 sweep 时最多阻塞下线序列 10s(权衡:换取不与 DB 池释放竞争);超时后 sweep 继续跑完但后续 DB 操作会收到 pool 关闭错误(仅日志噪音)。
4. 共享蓝绿网关 :8782 仍跑旧构建;九项修复随下一轮例行部署生效,部署后应观察一次真实 `plan sweep completed` 日志。
5. E-B2(货币批次大小可配置)与"串行 LIMIT 200 对非套餐厂商行的挤占"均未处理(后者为既有行为)。
