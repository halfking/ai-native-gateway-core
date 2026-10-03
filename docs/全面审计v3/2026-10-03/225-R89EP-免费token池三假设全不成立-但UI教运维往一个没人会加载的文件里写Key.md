# 225 号｜R89-EP：免费 token 池的三个假设全部不成立，但 UI 教运维往一个没有任何进程会加载的文件里写 Key

- 日期：2026-10-03
- 轮次：R89-EP
- 起点：台账第 32 项「免费 token 池（扫描/注册/聚合）」长期标 **「部分」**，
  证据只有 57/59 两份。objective 的原话是
  「**免费的 token 资源自动扫描、注册、和聚合使用，做为一个标准的供应商池子来处理**」。
- 方法：先查域接线（86 号判别点）→ 逐个假设坐实/证伪 → 转到**面向运维的文案**这一层
- **本轮新增待裁决 1 条（P2，待裁决 91）** + **守卫新增 1 条测试**
- **零生产代码改动**（改的是下发给 UI 的操作指引，按纪律登记待裁决）

---

## 一、结论先行

**UI 告诉运维把免费池的 API Key 写进 `config/free-pool.env`，而全仓没有任何进程会读这个文件。**

`admin/free_pool_extra.go` 的 `acquisitionMethods` 数组（`:235`）是**直接下发给前端 UI 的
操作指引数据，不是注释**。其中三处：

```
:239  将 API Key 写入 71 的 config/free-pool.env，由定时任务或「导入环境变量 Key」注入池子
:240  将 Key 写入 /opt/llm-gateway/config/free-pool.env（如 OPENROUTER_API_KEY=sk-...）
:300  凭据仅通过 config/free-pool.env（gitignore）或运行时环境变量注入，encrypt_secret 写入 DB
```

而**读取方不存在**，三条独立路径都查过：

1. **代码侧**：`collectEnvProviderConfigs`（`admin/routing.go:5220-5265`）是
   `/api/free-pool/import-env`（`handler.go:1323` → `routing.go:4851-4875`）的唯一数据源，
   它**只用 `os.Getenv`**，零文件读取；
2. **部署侧**：`installer/templates/compose.yml:71` 与 `installer/cmd/.../embeddata/compose.yml:71`
   都是 `env_file: .env`，**不是** `free-pool.env`；
3. **installer 侧**：`git grep free-pool -- installer/` 全文只在**一个 SQL 注释**里出现
   （`embeddata/startup/743_normalize_provider_protocol.sql:11`），与 env 文件无关。

⇒ **运维照做后**：把 key 写进那个文件 → 重启网关 → 点「导入环境变量 Key」
⇒ 端点返回 `{"mode":"env","candidates":0,"registered":0}`，**池子里一个凭据都没有**，
而 UI 显示的是一个 **200 的成功响应**。

---

## 二、⚠️ 本轮先证伪了自己的三个假设（这是本轮的一半产出）

进这一域前我按 86 号（`nodestatecache` 整包未接线）的判别方式，逐个查了三个最可能的缺陷形态。
**三个全部不成立**：

| 假设 | 判别方式 | 实测 | 结论 |
|---|---|---|---|
| ① `freediscovery` 域构造了但没启动（86 号形态） | `git grep "scanScheduler" -- cmd/gateway/main.go` | `:3119` 构造、`:3124` **Start**、`:7713` **Stop** | ❌ 接线完整 |
| ② 扫描结果没有变成凭据的路径 | `git grep "freediscovery\.\|ImportService" -- '*.go'`（排测试） | `admin/free_discovery.go:45` 构造 `ImportService`，`:470` 调 `importer.Import` | ❌ 有生产调用方 |
| ③ 注册时模型名不做归一化 | 读 `persistFreePoolRegistration`（`routing.go:5311`） | `:5341-5353` trim + 去空 + 去重 | ❌ 已做 |

顺带查了 `SetFreeDiscovery`（`main.go:3108` 有调用）、`scan_scheduler.go` 的 worker
（启动即跑 + 30s/60s 重试阶梯 + Stop-before-Start 守卫 + panic 收口，`:125-189`），
**全部完整**。

⇒ **objective 第 32 项在「扫描/注册/聚合」三个动词上都是接通的。**
⇒ 台账把它标「部分」是**基于 2026-10-01 的证据不足**，而不是这三个环节有缺陷。
⇒ 真正的缺口在**第四个环节：运维怎么把 Key 交进来** —— objective 没写，但 UI 写了。

⚠️ **可复用的做法**：「整包未接线」是上一轮（86 号）花大力气坐实的形态，
**但它不是这个域的默认结论**。**先查再怀疑**比**先怀疑再查**便宜得多，
而且查完的"不成立"本身就是交付（它让台账的「部分」可以被更新）。

---

## 三、定级 P2 的三条理由

| 维度 | 事实 |
|---|---|
| 不失败 | 端点返回 200，不报错 |
| **不波及** | 只影响**新注册**免费凭据的这条路径；已入库的凭据不受影响 |
| **理论知道实际不知道** | UI 明确写了文件名与绝对路径 ⇒ 运维会**确信自己配好了** |
| **无痕迹** | `candidates=0` 是正常值，无日志、无告警 |
| **无受害证据** | ⚠️ **本轮无法证实真的发生过**：本机无网关进程、无法查真库该池是否有凭据 |

⇒ **不是 P1**（无数据损坏、无请求失败、无安全面、**且未证实发生**），
但**比待裁决 60 那条更严重**：

| | 待裁决 60（`docs/hooks/prompt-optimization.md`） | 本条 |
|---|---|---|
| 面向 | 开发者/部署者 | **运维的可执行操作** |
| 形态 | 设了环境变量「不会有任何效果」 | 「**照做了，但什么都没发生**」 |
| 面 | 1 个 Hook | 免费池的**全部** env 注入路径（4 个端点共用 `collectEnvProviderConfigs`） |

---

## 四、守卫：`internal/sqlguard` 新增 `TestOperatorFacingEnvFileClaimsAreReal`

**判据**：面向用户的文案里提到的每一个「外部载体」，必须在本仓有真实的**读取方**。

两条设计要点（都是前几轮踩出来的）：

1. **「提到」与「读取」必须分开**。`findFileReaders` 只认
   `os.Open(` / `os.ReadFile(` / `ioutil.ReadFile(` / `godotenv.` 这类**打开动作**，
   并显式排除 `admin/free_pool_extra.go` 本身（那三处是**下发给 UI 的数据**，不是读取方）。
   若不排除，判据会被自己要抓的对象满足 ⇒ **恒绿**。
2. **锚点会腐烂**。三处登记都带 `文件:行号`，门每次校验该行仍含该文件名；
   文案改了/删了 ⇒ 报「登记表需要重新确认」而**不是**「缺陷已修复」。

**豁免显式且可自我失效**：`knownUnreadEnvFileClaims`（承 223/224 号的取舍）。

### 两条负控（全部实测）

| 负控 | 手法 | 结果 |
|---|---|---|
| **NC-F** | 移除 `knownUnreadEnvFileClaims` 豁免 | ✅ 转红，三条文案逐条点名 |
| **NC-G** | 造一个真读取方 `domains/probe225/probe.go`（`os.ReadFile("config/free-pool.env")`） | ✅ 豁免**自动失效**、门转绿并打印 `有 1 个读取方：domains/probe225/probe.go`；探针已回收 |

⇒ NC-G 是"豁免可自我失效"这条设计的**实证**，不是声明。

---

## 五、诚实边界

- **本机无网关进程、无 PG** ⇒ **无法证实**真的发生过运维照做的情形，
  也**无法查**真库里该池是否有凭据。定 P2 是基于「机制成立 + 面向运维 + 无痕迹」，
  **不是基于受害者**。
- **未核** `config/free-pool.env` 在**部署侧**（仓外的 ansible/systemd unit）是否被别的东西加载
  ⇒ 若有，本条的"无读取方"结论需要收窄。已核的只有仓内三条路径。
- **未核** UI 前端 `FreePoolView.vue` 是否也显示了同样的文案（本轮只核了后端下发的数据源）。
- `git grep free-pool -- installer/` 的结果里有一条**二进制文件命中**
  （`installer/llm-gw-installer`）⇒ **未核**该二进制内是否嵌了 free-pool.env 的加载逻辑。
- `go test ./...` 全量未跑；`core.hooksPath` 未设 ⇒ 未经 pre-push 门；**CI 从未运行**。
- 本轮**未改任何生产代码**；守卫集合仍 **14 个包**（`sqlguard` 已在 `GUARD_PACKAGES`）。

---

## 六、编号与去向

- **新增待裁决第 91 条（P2）**：`admin/free_pool_extra.go:239/:240/:300` 三处下发给 UI 的
  操作指引让运维把 Key 写进 `config/free-pool.env`，而全仓无任何读取方
  ⇒ 照做后 `import-env` 返回 `candidates=0` 且报 200 成功。
  **修法（文案侧，三处）**：改成「把 key 注入**进程环境变量**（docker `env_file` /
  systemd `EnvironmentFile`），再点『导入环境变量 Key』」。
  ⚠️ **本代理不擅自动手**：改的是下发给 UI 的运维指引，需产品/运维确认。
- **台账第 32 项可更新**：扫描/注册/聚合三个环节接线完整（见 §二），
  「部分」应改为「**有**（225 号补核三个环节的接线），**附 P2 待裁决 91**」。
- **守卫**：`internal/sqlguard` 新增 1 条测试，集合仍 14 包。
- **playbook**：§179–§180。

---

## 七、playbook §179–§180

**§179 「上一轮花大力气坐实的缺陷形态」不是这一域的默认结论。**

225 号进免费 token 池时，脑子里带的是 86 号（`nodestatecache` 整包未接线，
花了整轮坐实"有实现、有目录、零生产调用方"）。于是本轮**先查了三个最可能的形态**：

| 假设 | 判别 | 实测 |
|---|---|---|
| `freediscovery` 构造了没启动 | \`git grep "scanScheduler" -- cmd/gateway/main.go\` | \`:3119\` 构造 / \`:3124\` **Start** / \`:7713\` **Stop** |
| 扫描结果没变成凭据 | \`git grep "freediscovery\.\|ImportService"\`（排测试） | \`free_discovery.go:45\` 构造、\`:470\` 调 \`Import\` |
| 注册时模型名不归一 | 读 \`persistFreePoolRegistration\` | \`:5341-5353\` trim + 去空 + 去重 |

**三个全部不成立。** 而真正的缺陷在**第四个、objective 没写但 UI 写了**的环节。

⇒ **先查再怀疑比先怀疑再查便宜**，而且查完的"不成立"本身就是交付：
  它让台账的「部分」可以被更新（本轮据此建议把第 32 项从「部分」改为「有 + 附 P2」）。
⇒ 判别式：**"X 域常见形态是 Y" 是一个假设，不是一条判据。**
  把它当假设去查（成本 3 条 git grep），别当判据直接开单（成本是一整轮的错误登记）。
⇒ 与 §153/§176 同源：**前一轮的结论不能替代对当前对象的查证** ——
  86 号证明了"未接线"这个形态存在过，不证明它在这个域还成立。

**§180 「面向用户的文案」是一个独立的审查面，而它最容易整体失明。**

代码侧的接线可以逐条查（§179 的三个 git grep），但**下发给 UI 的操作指引数据**
（`admin/free_pool_extra.go` 的 `acquisitionMethods` 数组）是**数据结构不是注释**，
它不在任何"实现"心智模型里 ⇒ 不会被"查接线"这个动作覆盖。

⇒ 本轮的具体形态：文案让运维写进 `config/free-pool.env`，
  而三条独立路径（代码侧 `os.Getenv` / 部署侧 `env_file: .env` / installer）**都没有它**。
⇒ **审查清单要加一项**：本仓有没有**下发给前端/运维的文案**（`summary`/`steps`/`detail`
  这类字段），它们提到的每个**外部载体**（文件、路径、环境变量、端点）是否真有读取方。
⇒ 与待裁决 60 同族，但**更严重一档**：
  60 是"设了变量没效果"（面向开发者）；这条是"**照做了但什么都没发生**"（面向运维的可执行操作）。
⇒ ⚠️ 写这类判据时，**"提到"与"读取"必须分开**：
  判据若只查"文件名的出现次数"，会被**自己要抓的那三处文案**满足 ⇒ **恒绿**。
  本轮 `findFileReaders` 只认 `os.Open(`/`os.ReadFile(`/`godotenv.` 这类**打开动作**，
  并显式排除文案所在的那个文件本身。
  ⇒ 配套的负控是**NC-G**：造一个真读取方，豁免必须**自动失效**、门转绿并打印读取方位置。
  这一条把"豁免可自我失效"从声明变成实证。
