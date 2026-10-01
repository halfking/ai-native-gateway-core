# 161 / R89-BW：附件严格模式开关是本仓的正面样本，但它在样例 env 里写错了取值形式

> 日期：2026-10-01
> 起手：从 159/160 号的上下文里读出 `responses.go:280` 的 `attachmentStrictMode()`，
> 顺着 objective「原则上所有的操作不要影响客户端的观感」去问：**附件存储失败会不会让请求失败？由什么决定？**
> 结论：**默认不会，且这个决定是被审计改出来的、写清了理由、并在样例 env 里登记了默认值——全链条完整。**
> ⚠️ **但样例 env 里那一行的取值形式与代码解析器不匹配（孤例）。** 定级 **P3 / 待裁决 66**。
> **未改动任何生产代码或数据库。**

---

## 一、正面样本：这个开关的形态是本仓标杆（§41 桶④）

`domains/streaming/attachment_pipeline.go:11-19`：

```go
// attachmentStrictMode: R35 (2026-09-17 audit P2) flipped the default to
// lenient. The storage layer's declared contract is "存储失败不应阻塞请求转发，
// 仅记录 warning"（domains/attachments/storage.go）, but the previous default
// (any value except "0") turned an attachments-dir outage into 503 for every
// image-bearing request — an availability single point with a client-retry
// path that cannot succeed. Strict stays available as an explicit opt-in.
func attachmentStrictMode() bool {
	return strings.TrimSpace(os.Getenv("LLM_GATEWAY_ATTACHMENT_STRICT")) == "1"
}
```

这段注释一次性满足了 §41 桶④ 的全部要求：

| 要求 | 本例 |
|---|---|
| 说出**是谁、哪一轮**改的 | 「R35 (2026-09-17 audit P2)」 |
| 说出**改前是什么行为** | 「any value except "0"」⇒ **曾经任何非 `0` 值都开严格** |
| 说出**为什么错** | 附件目录故障 ⇒ **每个带图请求 503**，「an availability single point with a client-retry path that cannot succeed」 |
| 引用**契约原文** | 引 `storage.go` 的「存储失败不应阻塞请求转发，仅记录 warning」 |
| 保留**显式 opt-in** | 「Strict stays available as an explicit opt-in」 |

⇒ **它同时对齐了 objective 的两条要求**：
「不要影响客户端的观感」（默认 lenient）与「有备用时用 think 模式转达而不中断」（失败只记 warning + `logCtx.EmitFailure`）。

**这类注释应当被复制，而不是被改。**

---

## 二、闭环核验：开关有登记，且默认值与代码一致

`envs.samples/01-gateway-core.env.sample:135-137`：

```
# URL 出站抓取与严格模式（安全项，默认关闭）
LLM_GATEWAY_ATTACHMENT_URL_OUTBOUND=false
LLM_GATEWAY_ATTACHMENT_STRICT=false
```

- ✅ **有登记**（不是藏在代码里的裸开关）
- ✅ **默认值一致**：代码 `== "1"` ⇒ 未设置时为 lenient；样例 `=false` ⇒ 也是 lenient
- ✅ **有测试**：`domains/streaming/attachment_pipeline_test.go`
- ✅ **有审计留痕**：`docs/audit/2026-09-17-r35-gap-audit-round.md`

⚠️ **对照待裁决 60**（`docs/hooks/prompt-optimization.md` 教人设无效开关）：
**那一条是「文档承诺了不存在的接线」；本条是「代码里的开关被如实登记了」** ——
**两者方向相反，本条是正面对照组。**

---

## 三、⚠️ 唯一的缺口：样例里那一行的**取值形式**与解析器不匹配

**代码只认 `"1"`**（`== "1"`），**样例却写成了布尔字面量 `=false`**。

⚠️ **陷阱**：运维看到这一行有值、且长得像布尔量，**想打开严格模式时自然会写 `=true`**
⇒ 而代码只认 `"1"` ⇒ **静默得到宽松模式**，与意图完全相反、且**没有任何日志或告警**。

### 但这是**孤例**，不是全仓惯例（这一点必须写清，否则会误报成系统性缺陷）

全仓 `Getenv(...) == "1"` 形式的开关共 **11 个**，它们在样例 env 里的写法：

| 开关 | 样例写法 | 与 `== "1"` 一致？ |
|---|---|---|
| `ENABLE_CMB_EXPIRE` | `VAR=`（空） | ✅ |
| `INSTALL_SKIP_ACTIVATION` | `=0` | ✅ |
| `LICENSE_NO_GRACE` | `VAR=` | ✅ |
| `LLM_GATEWAY_LICENSE_ACTIVATED` | `VAR=` | ✅ |
| `LLM_GATEWAY_STATS_INBOX_CONSUMER` | `VAR=` | ✅ |
| `LLM_GATEWAY_UPSTREAM_PROXY_STRICT` | `VAR=` | ✅ |
| `STICKY_MULTILEVEL_DEBUG` | `VAR=` | ✅ |
| `CFG_DUMP_ALLOW_SECRETS` / `NO_INDEX` / `SKIP_SEED` | （样例中未出现） | — |
| **`LLM_GATEWAY_ATTACHMENT_STRICT`** | **`=false`** | ❌ **唯一写布尔字面量的** |

⇒ **其余 10 个都用「空值或数字」，与解析器一致；只有它写了 `false`。**
**这是一个单行文档缺陷，不是系统性约定冲突。**

⚠️ **按 §53 明确写出范围**：**受影响的是「照着样例猜布尔值去开严格模式」这一个操作**，
**不是「所有 `== "1"` 开关都教错了」**。

---

## 四、建议（待裁决 66，P3）

**一行改动**（二选一，都是把话说全）：

1. **把样例改成与解析器同形**：`LLM_GATEWAY_ATTACHMENT_STRICT=0`，并在注释里写
   「**填 `1` 开启严格模式（附件存储失败即拒绝请求）；其它值一律为宽松**」；
2. **或让代码接受布尔字面量**：`v := strings.ToLower(strings.TrimSpace(os.Getenv(...))); return v=="1"||v=="true"||v=="yes"`
   —— 兼容写法，但**扩大了接受的取值面**，需要考虑是否要连带处理其余 10 个开关。

⚠️ **不擅自动手**：改 `envs.samples/*.env.sample` 属于**对外可见的部署配置样例**，
且方案 2 会改变既有部署的行为面。**按纪律登记待裁决。**

**我的倾向是方案 1**：它只改一行注释/取值，不碰任何运行时行为，风险最小；
而方案 2 的一致性收益（接受 `true`）远小于它引入的「11 个开关取值面不一致」新问题。
