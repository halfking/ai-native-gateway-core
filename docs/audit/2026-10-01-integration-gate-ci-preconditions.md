# integration-gate CI job —— 重新启用的前置条件

ci-gate-preconditions: NOT-SATISFIED



日期：2026-10-01
状态：**未接入 CI**（harness 已实现并本地验证，workflow 里的 job 已移除）

## 结论

`scripts/audit/run-integration-gate.sh` 是**可用**的：它在一次性库上跑 `./db`，
实测 68 PASS / 5 SKIP / 0 FAIL，并且会拒绝把「全部 skip」报成绿。

围绕它的 `.github/workflows/integration-testcontainers-ci.yml:integration-gate`
job 是**不可用**的，且不是「未验证」，而是**三个彼此独立的必败原因**。已在
round 43 自审中移除。理由逐条附证据。

## 三个必败原因

### 1. 镜像 tag 不存在

原 job 写死 `registry.internal.example.com/kx-citus-pg17:offline-arm64`。

实际存在的 tag（`docker image inspect` 本机运行中的 `llm-gateway-pg` 容器）：

```
registry.internal.example.com/kx-citus-pg17:13.3.0-vector-arm64
```

`offline-arm64` 来自对别处的 grep 推断，**从未被 pull 过**。

### 2. 架构不匹配（即使 tag 改对也必败）

```
$ docker image inspect 8e59f08c07e1 --format '{{.Os}}/{{.Architecture}}'
linux/arm64
```

GitHub 托管的 `ubuntu-latest` runner 是 **x64**。arm64 镜像在 x64 runner 上
`docker run` 直接 exec-format 失败，PostgreSQL 起不来。

本机是 arm64（`uname -m` → `arm64`），所以本地能跑通**恰恰是**这个 bug 在本地
不可见的原因。

### 3. 缺 `PG_PASSWORD`

容器以 `POSTGRES_PASSWORD=ci` 启动，但 job 的 `env` 里没有 `PG_PASSWORD`。
harness 构造出的 DSN 因此无密码（`run-integration-gate.sh:77-81`），并在
自己的 DSN 预检处 `die`。

本机实测对照：

```
$ psql "postgresql://llm_gateway@127.0.0.1:5432/postgres" -tAc 'SELECT 1'
psql: error: ... fe_sendauth: no password supplied
$ psql "postgresql://llm_gateway:<pw>@127.0.0.1:5432/postgres" -tAc 'SELECT 1'
1
```

## 同批发现的两个小缺陷

- **`services: docker:24-dind` 是死配置。** job 的所有 step 调的是 runner
  自带的 `docker` CLI；workflow 从未导出 `DOCKER_HOST`，所以 dind 那个 daemon
  从头到尾没被用过。它让人误以为容器跑在 dind 网络里。
- **失败 artifact 指向单一 `/tmp/itgate-run.log`。** CI 里的 for 循环每个包都
  写同一个文件，上传的 artifact 只会是最后一个包的日志。

## 重新接入前必须逐条落实

> **2026-10-02 更新：第 1、2、3 条已实测满足。** 本节原先把「amd64 镜像」写成
> 一个需要外部构建基础设施的前置条件——**那个判断是错的，从未被验证过**。
> 实际去 registry 查了一次，镜像早就在那里。详见本文件末尾的「amd64 镜像实测」。
>
> 状态行仍是 `NOT-SATISFIED`，且必须如此：守卫
> `TestWorkflowDoesNotShipAnUnrunnableGateJob` 是**双向**的——标记 SATISFIED 而
> workflow 里没有接回 job 会直接失败。现在 job 已被移除，第 4 条（registry 凭据）
> 也只能由 CI 侧验证。**不把状态行翻成 SATISFIED，是当前事实，不是遗漏。**

1. ~~**有一个 amd64（x86_64）可用的 `kx-citus-pg17` 镜像**，且 tag 已
   `docker pull` 验证过。不能用 `:*-arm64` tag 跑 x64 runner。~~
   **已实测满足**：`registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64` 存在，
   manifest 的 platform 是 `linux/amd64`，已 pull 并在本机跑起来（见文末）。
2. ~~**镜像 tag 与 `PG_USER` 都实测过。**~~ **已实测满足**：以
   `POSTGRES_USER=llm_gateway` 启动 amd64 容器并用该用户连上通过。
3. ~~**在 job 的 `env` 里显式导出 `PG_PASSWORD`**，与容器的
   `POSTGRES_PASSWORD` 一致。~~ **已实测满足**：门禁在 amd64 容器上
   `exit=0 PASS=112 SKIP=0 FAIL=0`。
4. **registry 凭据**：本仓现存的命名惯例是
   `PG_CONTRACT_*_DATABASE_URL` / `TEST_DATABASE_URL`（见
   `.github/workflows/sessionforensics-ci.yml`）。原 job 用的
   `CI_PULL_TOKEN` / `CI_REGISTRY_USER` 是我发明的名字，仓内不存在。
5. **包列表必须来自实际带 `go:build integration` 的文件**，不能手写。
   已实测：`internal/trace` 有 **0** 个 integration 测试文件，但
   `go test -tags=integration ./internal/trace` 会报 31 PASS（全是普通单测）。
   harness 原本只看 `NPASS==0`，因此会对这个包报「GATE OK」而实际零
   integration 覆盖。此项已在 harness 内修正（见脚本
   `GATE 目标包无 integration 测试` 分支）。

## 状态行与守卫

文档头部有一行机器可读的状态：

```
```

守卫 `TestWorkflowDoesNotShipAnUnrunnableGateJob` 按它双向判定：

| 状态行 | workflow 里有 gate job | 守卫 |
|---|---|---|
| `NOT-SATISFIED` | 否 | 绿（当前状态） |
| `NOT-SATISFIED` | 是 | **红**：那个 job 三个原因必败 |
| `SATISFIED` | 是 | 绿 |
| `SATISFIED` | 否 | **红**：状态与实际不符 |

把状态行改成 `SATISFIED` 因此不是写个备注，而是一次承诺：守卫会一直红到
job 被加回来。这样「前置条件已满足」必须由人显式落成一行，而不是靠记忆。

### 守卫自身踩过的坑（变异验证时抓到的）

第一版守卫用裸 `strings.Contains(workflow, "run-integration-gate.sh")` 判
「job 是否接上」。而我留在 workflow 里的移除说明**恰好在注释里写了这个脚本
名**——于是注释满足了守卫，**job 真的加回去时守卫是绿的**。这正是「守卫被
注释满足」的形状。改用 `active()` 剥掉 `#` 注释行后再匹配。

同一个坑在本轮出现两次（另一次是 `TestJsonbLintParsesObjectTables` 的
`t.Log` no-op），所以现在每次写完守卫都跑一遍「故意改坏 → 必须转红」。

## R76 补记：DB 变量名契约（2026-10-01，21ea84833 之后）

前置条件第 1–4 条讲的是「job 怎么起得来」，但漏了一条**门本身能不能被叫醒**。

`TestGateInjectsEveryDBCredentialName` 的注入清单是从仓内按后缀
（`_DATABASE_URL` / `_DB_URL` / `_PG_URL` / `_ISOLATED_DB_URL`，外加
`DATABASE_URL` 与 `TEST_PG_DSN` 两个精确名）**推导**出来的，而它的第二遍扫描
（"文件里含 env 查找时，任意全大写字面量都算"）同样要过这道后缀过滤。所以
**任何以 `_PG_DSN` 结尾的变量名都不在契约内**——harness 永远不会注入它。

R76 开工前实测：仓内有 **5 道真库门**处在契约之外，靠一个自己发明的
`_PG_DSN` 变量名门控，包含

- `deploy/sql/verify/supplier_errors_pg_test.go`（仓库自有的 supplier 错误真库门）
- `domains/hooks/handoff/confirmation_pg_semantics_test.go`（R76 的 P0 级门）
- `db/request_logs_view_schema_test.go` / `db/view_schema_v2_contract_test.go`
- `domains/hooks/observability/telemetry/request_class_pg_test.go`

它们在 CI 上**结构性沉睡**，却照样以 `ok` 的形式出现在报告里——正是本文件
上一节引用的那句「A gate that only injects a subset of the names is not a weak
gate; it is a gate that certifies nothing about the files it leaves asleep」，
只不过这次漏的是**注入侧**而不是**读取侧**。

**已修**：五个文件全部改为优先读 `TEST_PG_DSN`（该名本就在注入清单里、且受
守卫强制），旧名保留为回退供手工运行。harness 与守卫**无需改动**，
`TestGateInjectsEveryDBCredentialName` 仍绿（15 个名字不变）。

> 这条同时是给本文件的一处提醒：前置条件清单里**没有**「我的门是否在契约内」
> 这一项，而它比前 5 条更早生效——门不在契约内时，job 就算接回来了也等于没接。

---

## amd64 镜像实测（2026-10-02）

前面所有轮次都把 §9-5 记作「依赖外部镜像构建基础设施 / 本机是 arm64 所以不行」。
**这个前提从未被验证过。** 查了一次 registry 就发现镜像早就在那里。

```
$ docker manifest inspect registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64
  -> 存在，platform = amd64（另有一个 unknown，是 attestation/provenance manifest）
$ docker manifest inspect registry.internal.example.com/kx-citus-pg17:13.3.0-vector
  -> no such manifest
$ docker manifest inspect registry.internal.example.com/kx-citus-pg17:offline-amd64
  -> no such manifest
```

**拉取必须显式指定平台**，否则会按宿主平台解析而失败：

```
$ docker pull registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64
  no matching manifest for linux/arm64/v8 in the manifest list entries
$ docker pull --platform linux/amd64 registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64
  Status: Downloaded newer image
```

**本机 arm64 上跑得起来**（Docker Desktop 的模拟）：

```
$ docker run -d --platform linux/amd64 -p 55432:5432 \
    -e POSTGRES_USER=llm_gateway -e POSTGRES_PASSWORD=... -e POSTGRES_DB=llm_gateway \
    -e POSTGRES_INITDB_ARGS="--encoding=UTF-8 --lc-collate=C.UTF-8 --lc-ctype=C.UTF-8" \
    registry.internal.example.com/kx-citus-pg17:13.3.0-vector-amd64
$ docker ps   -> Up (healthy)
$ docker logs -> starting PostgreSQL 17.10 (Debian 17.10-1.pgdg13+1) on x86_64-pc-linux-gnu,
                 compiled by gcc (Debian 14.2.0-19) 14.2.0, 64-bit
                 database system is ready to accept connections
```

**门禁在它上面端到端跑通**：

```
$ PG_CONTAINER=llm-gateway-pg-amd64 PG_PASSWORD=... \
    bash scripts/audit/run-integration-gate.sh ./autoupdate
  [coverage] integration-only test files=1
  [populated] shape=installer relations=435 (floor=400)
  exit=0  PASS=112  SKIP=0  FAIL=0
  real 0m52s
```

### 剩下没满足的

| # | 条件 | 为什么本机无法验证 |
|---|---|---|
| 4 | registry 凭据要用仓内真实约定（`PG_CONTRACT_*_DATABASE_URL` / `TEST_DATABASE_URL`） | 需要 CI secrets，本地没有 |
| — | job 接回 workflow | 状态行翻 SATISFIED 的前提；等 #4 落实后同一次提交里做 |

所以 §9-5 的**镜像部分已经完成**，剩下的是纯 CI 侧接线，不再是「基础设施不可得」。
