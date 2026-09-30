# 40 — R70：更正 R69 的 CI 结论 + 新增独立 `audit-guards-ci` job

> 2026-10-01 03:59 起，基线 `c1c0b4cec`（R69 推送后的合并态）。
> 轮次性质：**更正 + 补一个更要紧的发现**。R69 结尾登记了「CI 侧仍未接入」
> 作为保持开放项，本轮去查证，发现自己那条结论**部分不准确**。

## 1. 更正：R69 说「CI 完全没有覆盖守卫」，不准确

R69 实测各 workflow 时只看了**显式列出的包**，没意识到 `go test ./...` 的
隐式覆盖。实测证据：

```
go test -tags=integration -run 'TestErrDiscard|TestEveryRowsLoop' \
    ./internal/errdiscard/ ./internal/rowsguard/ -v
--- PASS: TestErrDiscard_NoNewDiscardingCallSites
--- PASS: TestEveryRowsLoopIsGuarded
```

`-tags` 是**追加**标签、不排除无标签文件，所以
`integration-testcontainers-ci.yml` 的 `go test -tags=integration ./...`
**确实会执行守卫**。

**教训**：「某 workflow 没提到这个包」与「CI 完全没覆盖」是两件事。
`./...` 会隐式覆盖所有无标签包。断言前必须把 `./...` 的实际覆盖范围跑出来，
不能靠读 YAML 里的包名列表去推断。

## 2. 但那个覆盖是偶然且间接的 —— 并由此发现一个更要紧的缺口

| # | 问题 | 后果 |
|---|---|---|
| 2.1 | `integration-testcontainers-ci.yml` 的 paths 过滤**不含 `admin/**`**（实测 8 个 workflow **无一**包含） | 改 `admin/` 不唤醒任何 workflow ⇒ 守卫也不跑。而 **R66–R68 的改动大半在 `admin/`** |
| 2.2 | 守卫要等 testcontainers 起 docker、30 分钟超时 | 守卫红了会混在重型 job 噪声里；job 因无关原因长红/跳过时，守卫信号一起消失 |
| 2.3 | `sessionforensics-ci.yml` **跑 `./admin` 测试**却不被 admin 改动触发 | admin 的测试只在别的路径变动时才顺带跑 |

第 2.3 条独立于守卫：**`admin/` 下的测试可以在一堆从未跑过它们的 admin 改动
之后照常合并。**

## 3. 修法：独立的 `audit-guards-ci` job

新增 `.github/workflows/audit-guards-ci.yml`，四条理由：

1. **触发面要对齐守卫的输入**。守卫扫的是**全仓** Go 源码，所以 paths 用
   `**/*.go` 而不是抄现有 workflow 的窄列表。任一 Go 文件改动都可能让某个
   守卫退化，触发面就必须是全仓。
2. **成本/信噪比**。守卫是纯静态分析（7 个包约 4s，无 DB、无网络），不该为
   它等 docker。
3. **独立性**。守卫红了必须单独红，不能被 integration job 的噪声淹没。
4. **登记门先行**。`guards-sync` 是「守卫体系本身没腐化」的前提，所以排在
   跑守卫之前——顺序不是随意的。

**本地实跑 CI 的等价命令链**（不是只验 YAML 语法）：

```
bash scripts/checks/guards-sync.sh && go build ./... && go test <7 包> -count=1 -timeout=180s
✅ 全部 4 个守卫包已登记（GUARD_PACKAGES 内 7 项）
ok internal/rowsguard / errdiscard / dbrows / jsoncol / paramguard / sqlguard / sqlreadguard
CI_EQUIV_EXIT=0
```

## 4. 本轮教训

1. **「workflow 没提到这个包」≠「CI 没覆盖」**。`go test ./...` 隐式覆盖所有
   无标签包。读 YAML 推断覆盖范围会得出错误结论——本轮就是这么错的。
2. **paths 过滤是执行路径的一部分，且最容易漏看**。一个 job 跑了 `./admin`
   的测试却不被 admin 改动触发，等于没有。**触发面必须与被测输入对齐。**
3. **重型 job 不适合承载轻量门**。守卫红在 docker/30min 的 job 里，信噪比趋
   于零。

## 5. 保持开放

- **`admin/**` 未被任何 workflow 的 paths 覆盖**（本轮 §2.1/§2.3）。新增的
  `audit-guards-ci` 用 `**/*.go` 覆盖了守卫这一层，但 **admin 自己的测试**
  （`go test ./admin/`）仍不在任何 CI 触发面内。要修需给
  `sessionforensics-ci.yml`（它已经跑 `./admin`）加 `admin/**` 触发路径——
  本轮未做，因为它会显著扩大该 job 的触发频率，属需要权衡的独立决策。
- 本轮只改 CI 配置，**未验证 workflow 在 GitHub 上的真实运行**（无 token /
  未触发）。等价命令链已在本地实跑通过，但「workflow 真的会红」这件事只有
  CI 第一次实跑才能证明。
- R66–R69 登记的既存红门与契约问题（`route_incidents` 的 503 变更是否被前端
  接受）仍未处理。
