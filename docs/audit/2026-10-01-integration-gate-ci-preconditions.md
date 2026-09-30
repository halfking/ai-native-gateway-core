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

原 job 写死 `registry.kxpms.cn/kx-citus-pg17:offline-arm64`。

实际存在的 tag（`docker image inspect` 本机运行中的 `llm-gateway-pg` 容器）：

```
registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-arm64
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

1. **有一个 amd64（x86_64）可用的 `kx-citus-pg17` 镜像**，且 tag 已
   `docker pull` 验证过。不能用 `:*-arm64` tag 跑 x64 runner。
2. **镜像 tag 与 `PG_USER` 都实测过。** 本机真实用户是 `llm_gateway`；原 job
   写的 `kxuser` 是猜的。
3. **在 job 的 `env` 里显式导出 `PG_PASSWORD`**，与容器的
   `POSTGRES_PASSWORD` 一致。
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
