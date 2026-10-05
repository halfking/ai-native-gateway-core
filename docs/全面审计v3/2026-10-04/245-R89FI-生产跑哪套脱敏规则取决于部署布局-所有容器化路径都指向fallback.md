# 245 号 R89-FI —— 生产跑的是哪套脱敏规则，取决于部署布局；而本仓所有容器化路径都指向 fallback

> **日期**：2026-10-04
> **性质**：普查 + 守卫（只报不拦）；**零生产代码改动**；
> **订正 244 号两处读数** + **订正 110 号一条结论** + **新增待裁决 107**
> **起手**：244 号交出了待裁决 106 的四条方案路。写方案表时我漏查了一件事 ——
> `domains/secretmask/secretmask.go:31-34` 写着「coupling summary masking to the
> **DB-backed, hot-reloaded safety Rule system** would add operational surface」，
> 这句话暗示仓库里存在**可从 DB 热加载规则**的系统。
> **如果主链路 detector 也能接那个，那 106 的方案表是缺了一条路的。**
> 结果：确实存在，而且比那更糟。
> **playbook**：§205-A / §205-B / §205-C

---

## §〇 结论先行

1. **🔴 F1：244 号那张表测的是 fallback 规则集，而生产是否走它取决于进程 CWD。**
   `cmd/gateway/goal_control.go:517-526` 用**相对路径** `configs/sensitive_patterns.yaml`
   构造唯一的 detector；加载失败只 `slog.Warn` 一句，然后返回内置的 14 条规则。
   ⇒ **两套规则集并存，选哪套由「进程工作目录下有没有那个文件」决定，没有任何其他信号。**

2. **🔴 F2：两套规则集不是包含关系，而是**互有对方没有的规则**（实测）**

   | 用例 | fallback（`NewPatternDetector`） | 文件（`sensitive_patterns.yaml`） |
   |---|---|---|
   | `ak-abcdefghij0123456789` | **HIT** | **MISS** |
   | `GB82WEST12345698765432`（IBAN） | **MISS** | **HIT** |
   | `sk-proj-` / `sk-ant-` / `sk-or-v1-` / `ghp_` / `xoxb-` / `AIza` | MISS | **MISS（一样漏）** |

   ⇒ **244 号的核心发现不变、且更强**：漏检不是 fallback 特有的，换成 yaml 也一样漏。
   ⇒ 但 `ak-` 与 `iban` 两行的读数**在两套上方向相反** ⇒ 244 报告已订正。

3. **🔴 F3：在本仓所有容器化部署路径下，容器里没有那个 yaml ⇒ 生产走 fallback。**

   | 部署产物 | 事实 | 判据 |
   |---|---|---|
   | `Dockerfile:53-59`（**runtime 阶段**） | `WORKDIR /app`；只 `COPY` 了二进制 + `config.example.yaml`/`config.yaml`；**无任何 `COPY configs/`** | 负控实测咬住（见 §三） |
   | `docker-compose.yml` / `.persistent` / `.quickstart` / `.v4-local` / `installer/templates/compose.yml` | **全部无 `configs` 引用** | 逐文件实测 |
   | `docker-compose.deploy-test.yml:108` / `.dev-research.yml:148` | 挂了 `./configs:/configs:ro` —— **挂到 `/configs`，而 WORKDIR 是 `/app`** ⇒ 相对路径解析成 `/app/configs/…`，**读不到** | 逐文件实测 |

   ⚠️ 极重要的一层：`Dockerfile:32` 的 `COPY . .` 在 **builder 阶段**，
   所以 builder 的 `/app` 里有 `configs/` —— **但那不是最终镜像**。
   只看 Dockerfile 前半段会得出完全相反的结论。

4. **🟡 F4：fallback 状态下热更接口是死的（订正 110 号）。**
   实测：fallback detector 的 `ReloadFromFile()` → `reload sensitive pattern config failed: no config path set`；
   同一行代码在 file detector 上 → `err=<nil>`。
   ⇒ `/api/admin/sensitive-words/reload`（`admin/sensitive_words_handler.go:46-57`）
   在 fallback 上**只能答 500**。110 号写的「规则集在生产确实可热更」**只对文件分支成立**，
   而它写这句时没有检查 fallback 分支。
   ⚠️ **要区分两件强度不同的事**：调 HTTP 接口是**可见失败（500）**；
   **直接改 yaml 文件是彻底无声的** —— 而后者才是运维的自然动作。

5. **🟡 F5：`sensitive_patterns.yaml` 里有大片规则解析器根本不读。**
   `parsePatternConfig`（`detector_config.go:75-82`）只遍历 `pii` / `secret` / `financial` 三类，
   每个 pattern 只读 `regex` 与 `group`。⇒ 以下**全部静默丢弃**：
   - **`medical:` 整类**（`medical_record` / `diagnosis` / `medication` 三条 regex 一条都不生效）；
   - 每条 pattern 的 **`confidence`**（yaml 给了，`patternDefinition` 没有这个字段）；
   - `global` / `performance` / `audit` / `whitelist` / `custom_rules` / `redaction_rules` 整节。
   ⇒ 「改 yaml 加一条医疗规则」看起来是配置行为，实际什么都不发生。

6. **🟡 F6：`CompositeDetector` / `CustomDetector` 零生产调用点** ——
   它们只被 `detector_test.go` 使用。**DB 热加载那条路在代码里并不存在**（F5 那些键是 yaml 键，不是 DB 表）。
   ⇒ **106 的方案表不用补这条路**（这是我起手时的猜测，**被证伪**）。

---

## §一 一次假否定（**起手就踩的坑，值得留档**）

第一次普查用的是 `git grep -c <symbol> -- '*.go' | ForEach-Object { $_.Line }`，
`CompositeDetector` / `CustomDetector` / `newSanitizePatternDetector` **三个符号全部零输出**。

**那是假的。** `git grep` 直接输出字符串，`.Line` 属性在字符串上不存在，
`ForEach-Object { $_.Line }` 返回 `$null`，于是**全部被吞掉**。

而 `detector.go:182-197` 就定义着 `CompositeDetector` —— 如果我信了那个零命中，
本报告会写「主链路没有任何自定义/组合检测器」。

⇒ 这次能发现，**只因为我先写了阳性对照**（§206 纪律）。
**管道本身就是被测对象的一部分**：`grep | ForEach-Object { $_.Line }` 是一个惯用写法，
而它在这条路径上恒返回空。**这不是「没搜到」，这是「搜了但把结果扔了」，两者输出完全一样。**

---

## §二 106 的方案表需要修订（**这是本轮对上一轮的实际反哺**）

244 号给了 R-A / R-B / R-C / R-D 四条路。245 号补了两条事实进去：

| 方案 | 244 号的描述 | 245 号的修订 |
|---|---|---|
| **R-B**（只放宽 `sk-` 字符类） | 改 `detector.go:132` 一行 | ⚠️ **要先确定改哪一套**。文件分支对应的是 yaml 的 `secret.api_key`（`\b(sk\|key\|token\|api\|bearer)[-_]?[A-Za-z0-9]{16,}\b`），**两处都要改**才是真的「改一处生效」 |
| **R-D**（缩小 detector 职责，只管 PII） | 零代码 | ⚠️ **代价被低估**：yaml 里有 `iban` / `passport` / `address` / `account` / `cvv` / `alipay` / `wechat` **七条是 fallback 没有的**。若生产走 fallback，这些**已经**不生效；R-D 要么顺带承认它们不存在，要么把它们补进代码 |
| **新增 R-E** | — | **先让 yaml 在生产真的生效**（COPY/mount 修好 + 相对路径改绝对或启动时校验），**再谈扩规则面**。在此之前改规则集都是改一个可能没在跑的东西 |

⚠️ **R-E 是本轮新增的一条**，且它的成本远低于 R-A/R-C（不动代码结构，只改部署产物与一处路径解析）。
⚠️ 但**本轮不拍板**：R-E 涉及部署产物与启动行为变更。

---

## §三 守卫与负控

### 守卫 1：`security/sanitize/deployment_pattern_config_test.go`（新增，**只报不拦**）
`Test245DeploymentShipsThePatternConfig` 逐条报告：Dockerfile runtime 阶段有没有把 `configs/` 带进去、
每个 compose 有没有把 `configs` 挂到**相对路径能命中的位置**。
**只报不拦的理由同 244**：修法是部署/启动行为的产品决策（待裁决 107）。

### 守卫 2：`Test245FallbackDetectorCannotHotReload`
实测并**钉死**「fallback detector 不可热更」这个事实（负向断言当前行为）。
⚠️ **刻意断言现状**：把它翻成失败就是修法，那是待裁决 107。
**守卫的作用是让「若哪天 fallback 突然能热更了」这件事被注意到**，而不是断言它应该这样。

### 负控 NC-D：临时给 `Dockerfile` 加 `COPY configs ./configs`
`MISSING` → **`OK: COPY configs ./configs (source list: configs)`**，咬住。
改后**已字节级还原**（SHA256 `E3BDEFB1…A52A8` 前后一致，`git diff` 为空）。

### 🔴 F7 **我自己的守卫一开始是瞎的（本轮第二有价值的部分）**

第一次跑，判据报的是：

```
OK: COPY --from=builder /app/bin/llm-gateway .
```

**这是完全错误的** —— 那行只拷一个二进制文件，`configs/` 没进去。根因：
`strings.Fields` 把该行切成 `["COPY","--from=builder","/app/bin/llm-gateway","."]`，
我的 `switch` 命中了 **`case "."`**，而**那个 `.` 是 COPY 的「目标目录」，不是「源」**。

⇒ **判据在最关键的那一条上给出了「通过」，而且输出看起来完全正常。**
⇒ 修法：解析 COPY 的参数时**丢掉最后一个 token（目标）**，只对**源列表**判定。
修完立刻变成正确的 `MISSING`，NC-D 随即咬住。

⚠️ 与 210 号那条「覆盖下限钉在过滤后的集合上」同族：
**「源」和「目标」长得一模一样（都是路径 token），而我按名字匹配，没按位置。**
**How to apply**：解析任何「A 后面跟 B」形式的命令/配置时，
**先确定哪个是主语**，再写匹配 —— 不要让两个同形的字段落进同一个 `switch` 分支。

---

## §四 待裁决 107（P1）—— 生产该跑哪套规则集

**性质**：**决策阻塞 + 部署影响**。
按 v3 两问自评：Q2a 满足（四条互斥方案）；Q2b 满足（成本量级差异显著）。

> **Q1（须产品/运维定调）**：`configs/sensitive_patterns.yaml` 到底是**运行时配置**
> （应当真的生效、可运维改），还是**仅供开发/测试**的样例文件（那就应当从名字与文档上
> 让人一眼看出它不进生产）？**当前它两者都不是** —— 名字像前者，部署像后者。

| 方案 | 做什么 | 成本 | 风险 |
|---|---|---|---|
| **R-E1** | 让它真的生效：runtime 阶段 `COPY configs/ ./configs/`；compose 挂载点改 `/app/configs` 或设 `working_dir` | **低**（只改部署产物，零 Go 代码） | yaml 里的 `medical`/`confidence` 等未解析键会给人「已生效」的错觉（⇒ 建议同批清理或补解析） |
| **R-E2** | 路径改绝对（env/flag 注入），并在加载失败时**硬失败**而非静默 fallback | 中（改 `goal_control.go` 启动行为） | 配错的部署会**起不来** —— 这是**故意**的，但要产品认这个取舍 |
| **R-E3** | 承认它不进生产：改名（如 `sensitive_patterns.sample.yaml`）并把 fallback 那 14 条**显式化**为唯一事实源 | 中 | 会**丢掉** yaml-only 的七条（`iban`/`passport`/`address`/`account`/`cvv`/`alipay`/`wechat`）⇒ 需先决定它们是否要 |
| **R-E4** | 维持现状 | 零 | **运维改配置静默无效**这条继续存在；110 号那条结论继续是错的 |

⚠️ **本轮不拍板**。⚠️ 无论选哪条，**244 的 `sk-proj-`/`sk-ant-`/`ghp_`/`xoxb-`/`AIza` 漏检都仍在** ——
那与本条**正交**，由**待裁决 106** 处置。

**与 106 的关系**：106 是「规则面够不够宽」，107 是「哪套规则面在跑」。
**先定 107 再定 106**，否则 106 的 R-B 会改到一个不生效的地方。

---

## §五 诚实边界

- **无 Docker、无 PG/Redis、未起真进程**。本轮全部关于「容器里没有 configs/」的结论
  来自**读 Dockerfile 与 compose 文件 + 一个纯字符串守卫**，
  **没有真正 build 过镜像、也没有进过容器确认文件缺失**。
  ⇒ 结论强度：*强烈提示，未在真实部署坐实*。
  ⚠️ **但「两套规则集不同」（F2）与「fallback 不可热更」（F4）是纯 Go 实测，可复现。**
- **未坐实「生产实际用哪个部署方式」**。若生产是裸机/systemd 直接跑二进制、
  且 CWD 恰好是仓库根，则 yaml **是**生效的 ⇒ F3 的适用性随之改变。
  ⇒ **F3 的准确表述是：在本仓提供的容器化路径下**成立。
- **245 号的假否定（F1 坑）发生在检索工具层，不是代码层**；
  已用阳性对照兜住，但**不能排除本轮其它检索也受同类影响** ⇒ 凡本轮「零命中」结论，
  一律应视为「该符号未被本轮判据命中」，而非「不存在」。
- `go test ./...` 全量未跑；`cmd/gateway` 顶层红 25 个（沿用基线，本轮未复测计数）。
  **CI 从未运行**。
- `security/sanitize` 与 `domains/hooks/handoff` **均不在 `GUARD_PACKAGES` 内**
  （沿用**待裁决 100**）⇒ 本轮两条守卫**都不在快速守卫门内**。
- 临时探针 `zz_probe245{,_b}_test.go` 已回收（`rm --` 走 mavis-trash）。

---

## §六 playbook

### §205-A **「这台机器上跑的是哪一套」必须查部署产物，不能从构造函数名推断**

244 号看到 `NewPatternDetector()` 就以为那是生产用的构造器。实际生产走的是
`NewPatternDetectorFromFile(相对路径)`，失败才回落到前者。
**两个构造器都在，差别是一行错误处理，而那一行决定整个行为。**

⇒ **How to apply**：断言「某对象在生产怎么被构造」时，
**从调用点往上追到 main**，并**把失败分支当成一等公民** ——
「加载失败时用 A，加载成功时用 B」是**两个不同的系统**，不是同一系统的两种配置。
⇒ 配套：**普查表的分母不能是被测对象当前状态**。244 的分母是「我手头这个 detector」，
  245 才发现那是两套里的一套。**凡存在 fallback/降级路径的组件，普查必须覆盖每一支。**

### §205-B **`COPY a .` 里的 `.` 是目标不是源；同形 token 落进同一个 `switch` 就是判据的盲区**

守卫第一次跑就报 `OK: COPY --from=builder /app/bin/llm-gateway .`。
根因是 `strings.Fields` 后那个 `.` 命中了「源是 `.`」的分支 —— **而它是目标目录**。
判据在最该报错的地方报了通过，**输出还长得完全正常**。

⇒ **How to apply**：解析 `命令 源... 目标` 形式的语法时，
**先按位置切分（最后一个 token 是目标），再按名字匹配**。
**绝不能让两个位置不同、但字面同形的 token 落进同一个分支。**
（同 244 的 §204-B、210 号的「宽集合与过滤后集合必须是两回事」：
**都是「按字面形态归类，忽略了形态所属的位置」。**）

### §205-C **一份配置里没被解析的键，比缺失的键更危险**

`sensitive_patterns.yaml` 里 `medical:` 整类、`confidence`、`redaction_rules`、
`whitelist`、`custom_rules` 全都写得清清楚楚、看着都像生效了，
而 `parsePatternConfig` **一条都不读**。运维加一条医疗规则不会有任何反应，也不会有任何提示。

⇒ **How to apply**：给一份配置加键之前，先确认解析器**真的会读它**。
**更值得做的是反向的**：写一个守卫，列出「配置里出现但解析器不认的键」并打印 ——
**配置与代码的漂移是双向的，「多写了」和「没接上」同样需要被发现。**
⇒ 顺带：`medical`/`confidence` 这些键的失效**在 106 与 107 的任何方案下都存在**，
它不是部署问题，是解析器覆盖问题。
