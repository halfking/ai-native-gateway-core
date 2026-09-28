# Handoff — vapeur 部署管线第三轮批判复审（2026-09-29）

> 上一轮见 `docs/audit/2026-09-28-vapeur-protocol-adaptation.md` 第六/七节。
> 本文件只覆盖第三轮（08-29 00:4x–01:0x）新增的事实、修复与未决项。

## 1. 一句话状态

本地网关运行在 **2.5.6.2313**（`git_sha=31e468a6`），vapEUR（provider 36）
live 探针实测 `healthy / 2071ms`；本轮落一处真代码修复（部署脚本前端失败静默
夭折）+ 一条新契约门，两条既有错误结论已在本文件与审计文档中明确纠正。

## 2. 关键事实（均绑定 build_seq / git_sha，换一轮即需重取）

| 项 | 值 | 取证方式 |
|---|---|---|
| HEAD（本轮工作时） | `18f5d9fa` | `git rev-parse` |
| 运行版本 | `2.5.6-31e468a6-20260928-2313` | `GET /version` |
| vapEUR live 探针 | `provider_id=36, status=healthy, latency_ms=2071` | `POST /api/admin/providers/36/test-now` |
| 凭据 126 | `healthy / 512ms / 2026-09-29 00:05:58` | `SELECT ... FROM credentials WHERE provider_id=36` |
| 凭据 13 | `unreachable`（`403 Organization is disabled`，2026-09-20 起） | 同上 |
| 前端构建 | `npm run build` → `BUILD_EXIT=0`，零代码改动 | 本轮实测 |

## 3. 本轮改动

| 文件 | 性质 | 说明 |
|---|---|---|
| `scripts/deploy-local.sh` | **真修复** | `build_frontend()` 失败不再静默：输出落 `$RUN_DIR/build-frontend.log`，失败回显末 40 行并 `die` |
| `scripts/deploy-local-frontend_test.sh` | **新增门** | 从已发布脚本抽取函数体驱动假 `npm` 失败，断言「致命 + 诊断可见 + 日志非空」，含 `--no-frontend` 对照 |
| `docs/audit/2026-09-28-vapeur-protocol-adaptation.md` | 文档 | 第八节：三条声明复核、口径纠正、live 探针打通、缺陷修复与自证 |

**门可信度**（本轮自己踩的坑）：首版 fixture 把 `.bin/vite` 建成不可执行空文件，
命中 `node_modules is missing` 的 warn 分支，`npm` 根本没被调用——门会以「通过」
形态测一个它没测的性质。改为可执行 stub 后重验：修复前红
（`surfaced no compiler diagnostic`）、修复后绿。

## 4. 两条必须带走的纠正

1. **「vue-tsc 类型错 ⇒ 改 test 文件」是错解**。真实根因是 vitest 4→5 升级后
   node_modules 未同步；`npm install` 后零改动即绿。改那两个 `.test.ts` 属于改错对象。
2. **「live 探针被 unknown format 阻塞」已过期**。该结论建立在错误密钥上，
   `.env.local` 修正后探针实测 healthy。引用旧结论前必须先核当前 build_seq。

## 5. 未决风险（勿当成已解决）

1. **构建失败仍烧 build_seq**：`bump_local_version` 先于 `build_frontend`，而
   `web/public/version.json` 由 bump 写入、前端构建依赖它 ⇒ 无法简单前移，结构性。
2. **密钥漂移无仓库护栏**：`.env.local` 被 gitignore，SK/CEK 同值漂移可复发；
   22b655463 标题称「治本」但 `git show --stat` 显示零代码。
3. **共享 PG 崩溃未定性**：今日 1 次（22:40:56 shutdown → 22:44:05 recovery，
   redo 9.35s），发生在部署完成 3 分钟后；「宿主 OOM」在 PG 日志中无直接证据，
   只是旁证推断，**不可写成结论**。
4. **并发会话改写同一工作区**：本轮运行版本被 2308→2313 覆盖、HEAD 前推 35 提交。
   任何结论必须绑定 git_sha，否则引用即失效。
5. **`v1:legacy:` 凭据依赖正确的 Fernet key**：现凭据解密正常，但一旦 `.env.local`
   再漂移，73 条 legacy 行会整体不可解 → enrichWithAPIKeys 全标不可用 → 全站 503。

## 6. 下一轮提示词

> 接着做 llm-gateway-go 部署管线的第三项未决风险：**给密钥漂移加 preflight 护栏**。
> 背景见 `docs/audit/2026-09-28-vapeur-protocol-adaptation.md` §8.5 第 2 条——
> `.env.local` 不受版本控制，SK/CEK 同值漂移曾导致全站 503 且仓库零留痕。
>
> 要求：① 先写一条会在现状下变红的门（断言 deploy 在 SK==CEK 或长度异常时拒绝
> 继续，而不是烧掉 build_seq 后才失败）；② 门必须驱动**已发布脚本**的真实函数体，
> fixture 要可执行——上一版的前车之鉴是空文件 stub 命中 warn 分支、门测了错的性质；
> ③ 改完跑 `bash scripts/deploy-local-frontend_test.sh` 与既有 8 条 deploy 契约测试，
> 全绿再提交；④ 提交前 `git fetch && git log origin/main..HEAD` 核对并发会话是否又
> 前推了 HEAD，取证与结论一律绑定 git_sha。
