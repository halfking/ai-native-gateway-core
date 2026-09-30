# Round 43 批判式自审：哪些是「声明完成」而不是真的完成

日期：2026-10-01
范围：`docs/handoff/20261001-baseline-generator-round.md` 所记录的 round 43 全部产出
方法：逐条回到文件与真实环境取证，凡「已验证」的说法必须能重跑出来

---

## 结论先行

round 43 的**数据库层工作是真的**：基线生成器真能跑（生成产物 apply 到空库
exit=0 / 660 relations）、applier 事务窄免经真库 A/B、538 守卫经真库双分支
验证、`./db` 门 69 PASS / 5 SKIP / 0 FAIL 且**真的执行了**（不是 skip）。

但 round 43 交付的**编排层（CI job）不是真的** —— 它不是「未验证」，而是
**三个彼此独立的必败原因**，一次都没跑过所以没人知道。加上 harness 内部
**两条静默假绿路径**，本轮共修掉 5 个「声明完成」的项，其中 2 个是我自己
上一轮亲手写的守卫。

下面每一条都附「怎么证伪的」。

---

## P0 · `integration-gate` CI job 三个原因必败（已移除）

### 1. 镜像 tag 不存在

原 job 写死 `registry.kxpms.cn/kx-citus-pg17:offline-arm64`。

```
$ docker inspect llm-gateway-pg --format '{{.Image}}'   # 本机在跑的容器
8e59f08c07e1
$ docker image inspect 8e59f08c07e1 --format '{{.RepoTags}}'
[registry.kxpms.cn/kx-citus-pg17:13.3.0-vector-arm64]
```

`offline-arm64` 来自对别处的 grep 推断，**从未被 pull 过**。

### 2. 架构不匹配（tag 改对也必败）

```
$ docker image inspect 8e59f08c07e1 --format '{{.Os}}/{{.Architecture}}'
linux/arm64
$ uname -m          # 本机
arm64
```

GitHub 托管 `ubuntu-latest` runner 是 **x64**。arm64 镜像在上面 `docker run`
直接 exec-format 失败，PostgreSQL 起不来。
**本机是 arm64，所以本地一切正常——这正是它在本地不可见的原因。**

### 3. 缺 `PG_PASSWORD`

容器以 `POSTGRES_PASSWORD=ci` 启动，job 的 `env` 里没有 `PG_PASSWORD`。
harness 因此构造出无密码 DSN 并在自己的预检处 `die`：

```
$ psql "postgresql://llm_gateway@127.0.0.1:5432/postgres" -tAc 'SELECT 1'
psql: error: ... fe_sendauth: no password supplied
$ psql "postgresql://llm_gateway:<pw>@127.0.0.1:5432/postgres" -tAc 'SELECT 1'
1
```

### 同批两个小缺陷

- `services: docker:24-dind` 是**死配置**：所有 step 调的是 runner 自带的
  docker CLI，workflow 从未导出 `DOCKER_HOST`，dind daemon 从头到尾没用过。
- 失败 artifact 指向单一 `/tmp/itgate-run.log`，而 CI 的 for 循环每个包写
  同一个文件 → 上传的 artifact 只会是最后一个包。

### 处理

**移除 job，不修。** 修它需要一个 amd64 的 Citus 镜像，仓内不存在。
改为「状态行 + 守卫」双向绑定，见 `2026-10-01-integration-gate-ci-preconditions.md`。

---

## P1 · harness 有两条静默假绿路径（已修，均经变异验证）

### 1. 启动迁移解析出 0 条，无任何报错

harness 用 `sed` 从 `runner.go` 抽取 `StartupFiles`。Go 源码一重排，锚点
失配 → **0 行、无 exit code 变化** → 循环什么都不做 → 门禁库静默退回
**328 relations** 的陈旧基线（而非 421）→ `db.ensure*()` 开始报
`relation does not exist`，读起来像产品缺陷。

证伪方式：把锚点改成不匹配的字符串。

```
  startup: applied=0 failed=0 missing=0
ERROR: 从 installer/internal/dbinit/runner.go 的 StartupFiles 里解析出 0 条迁移。 ...
```

修法：`sf_total = applied+failed+missing`，`==0` 致命退出，并加 **100 条
地板**（实测注册迁移 173 条）以挡住部分失配。
`TestGateRejectsSilentZeroStartupParse` 变异验证：
`sf_total == 0` 改成 `== -1` → 转红。

### 2. 零 integration 覆盖的包会报「门禁通过」

`NPASS==0` 的空跑判定挡不住这种情况：**普通单测也会报 PASS**。

```
$ grep -rl 'go:build integration' internal/trace/*.go | wc -l
0
$ go test -tags=integration -v ./internal/trace | grep -cE '^\s*--- PASS'
31
```

`internal/trace` **0 个 integration 测试文件**，却报 31 PASS —— 而它就在
我上一轮写进 CI 的包列表里。

修法：用 `go list` 的**集合差**（带 tag 减去不带 tag）判定，让 go 自己去求值
build 约束，而不是在 shell 里重写一遍 tag 解析。

**中间踩了两个坑，都记在脚本注释里：**

1. 先写成 `grep '^//go:build.*integration'` —— 被否定约束骗过：
   `//go:build !nintegration` 同样含该子串。
2. 再写成 `go list` + 对输出做 `-f` 存在性判断 —— `go list` 返回的是
   **包内相对路径**（`db_migration_538_integration_test.go`），从仓库根做
   `-f` 判空，结果这条守卫**拒绝所有包**，包括真有 integration 测试的 `./db`。
   这是我跑真实 harness 才暴露的，读代码看不出来。

`TestGateRejectsPackagesWithoutIntegrationTests` 断言必须用 `comm -13` 差集，
并**显式禁止**退回 grep 解析 tag。变异：把差集换成 `grep -rlE` → 转红。

---

## P2 · 三项「说了不算」的守卫/脚本问题

### 1. `TestJsonbLintParsesObjectTables` 的第二个断言是 `t.Log` —— no-op

```go
if !regexp.MustCompile(`...`).MatchString(src) {
    t.Log("注意：未见显式的目录遍历/Glob，请确认 ...")
}
```

`t.Log` 从不失败。而且这个正则**实际是匹配的**，所以那行连提示都不会打印。
上一轮汇报里我把它算进「20 项守卫通过」，属于**夸大**。

改成对具体不变式的断言：列映射目录必须由
`filepath.Join(..., "sql", "objects", "tables")` 得出。
变异：把 `"tables"` 换成 `"indexes"` → 转红（改之前不会）。

### 2. `TestBaselineDumpScriptIsRunnable` 名不副实

它只检查被 source 的库文件**路径可解析**，从不运行生成器 —— 运行需要一个
已知良好的 SSOT 库，而本仓没有（本地开发库是迁移到一半的中间态）。

已改名为 `TestBaselineGeneratorLibraryResolves`，并在注释里显式写出
**未被任何测试覆盖的那半边**：生成产物 apply 到空库 exit=0 / 660 relations
是 2026-10-01 **手工验证**的，不是测试保证。

### 3. 宿主 `psql` 硬依赖 + 报错指向错误的原因

第一版无条件 `psql "$DSN"`。宿主没有 psql 二进制时，它会走进一个
**把原因说成「密码不对」**的分支，把人引到错误方向。
改为先探测二进制，缺失时回退到容器内 psql（只需 `SELECT 1`，故用多架构
的 `postgres:17-alpine`，而不是 arm64-only 的项目镜像）。

---

## P2 · `set -u` 下变量紧跟中文标点会崩（harness 自身 bug）

变异验证 relations 地板时炸出来的：

```
$ bash -c 'set -u; X=5; echo "值 $X。结束"'
bash: 行 1: X?: 未绑定的变量
$ bash -c 'set -u; X=5; echo "值 ${X}。结束"'
值 5。结束
```

bash 在 UTF-8 locale 下把全角标点并进了变量名。受影响的是 `GATE_DB` 与
`GATE_MIN_RELATIONS`。全部改为 `${VAR}`，并加了守卫
`TestGateDoesNotRequireHostPsql` 里的正则断言**禁止**再出现
`\$VAR` 紧跟非 ASCII 的写法。

---

## 上一轮我自己的两处「假绿」守卫（本轮修）

| 守卫 | 形状 | 怎么发现的 |
|---|---|---|
| `TestJsonbLintParsesObjectTables` | `t.Log` 提示，从不失败 | 读代码时发现分支永不触发 |
| `TestWorkflowDoesNotShipAnUnrunnableGateJob`（本轮新写） | **注释里写了 `run-integration-gate.sh`**，裸子串匹配被移除说明满足 | **变异**：把 job 加回去，守卫仍然绿 |

第二条尤其难看：它是本轮为了修 P0 才写的，写完第一次变异就发现它是假绿。
两次都指向同一条规则 —— **守卫的判据必须打在产物的活跃内容上，
写完必须做「故意改坏 → 必须转红」**。

---

## 本轮新增的变异验证记录

| 变异 | 期望 | 结果 |
|---|---|---|
| harness 跑 `./internal/trace` | 0.16s 内 die，不建库 | ✅ |
| `GATE_MIN_RELATIONS=500`（实测 421） | die 并给出地板诊断 | ✅（先暴露了 `set -u` bug，修后重跑通过） |
| 破坏 `sed` 锚点 | `applied=0` 立即 die | ✅ |
| `sf_total == 0` → `== -1` | Go 守卫转红 | ✅ |
| `RELS < GATE_MIN_RELATIONS` → `RELS < 0` | Go 守卫转红 | ✅ |
| `comm -13` 差集 → `grep -rlE` | Go 守卫转红 | ✅ |
| jsonb 目录 `tables` → `indexes` | Go 守卫转红 | ✅ |
| job 加回 workflow（NOT-SATISFIED） | 转红 | ✅（第一版守卫此处**是绿的**，修后通过） |
| 状态行改 SATISFIED 但 job 不在 | 转红 | ✅ |
| 删掉状态行 | 转红（t.Fatal） | ✅ |

**关于第一版守卫那次失败**：我的变异脚本本身一度是空操作（`sed` 报
`unescaped newline inside substitute pattern`，输出文件根本没生成），
而我当时那个「锚点已破坏 ✓」的检查只比较了两个不同词的计数，**必然不等**，
所以它打印了 ✓。改用 python 确定性替换 + `diff` 断言差异存在之后，
变异才真正生效。**变异脚本本身也要验证它变异了。**

---

## 没有被本轮证明的事（不要当成已完成）

1. **CI 上跑过一次 integration 门** —— 没有。job 已移除，重启前置条件见
   `2026-10-01-integration-gate-ci-preconditions.md`。
2. **生成器产物 apply 到空库** —— 2026-10-01 手工验证过一次
   （exit=0 / 660 relations / 2491 indexes），**无测试保证**，改生成器不会被自动发现。
3. **fresh-install 链路整体可用** —— 仍然不成立。门禁库 apply 启动迁移时
   仍有 14 条不落地（含 802 `relation "public.session_turn_details" does not
   exist`），这是已知缺口，harness 报出来但不因此判红。
4. **802 索引的必要性** —— 注释声称的前提（缺索引 → 167 万行顺序扫描）
   已被实测推翻（`idx_request_logs_tenant_task_ts ON ONLY public.request_logs
   (tenant_id, gw_task_id, ts DESC)` 存在）。按用户先前选择**只报告不修**。
5. **`sql/objects/` 定位** —— 仍待拍板（偏归档）。
