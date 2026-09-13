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
