<!--
裁剪自 docs/02-resources/research/pricing/raw/google-gemini.md（第 195-206 行那一段），
**只保留页形**：一个模型一张小表、表头是「空 | Free Tier | Paid Tier」、
数据行第一列是维度名（Input price / Output price / …）、而**表上方那段文案里没有
模型名**。

为什么裁剪而不是直接用那份实抓文件：那是会被重新抓取覆盖的活数据，判据依赖它
等于依赖"哪天页面改版判据自己变红"。本文件只固定**页形**，那才是这条诊断要区分的
东西。

★ 这份夹具的存在本身就是一条记录：2026-10-05 实测，google-gemini.md 的 Gemini 3
家族那 250 多行，模型名在 jina 转换后的 markdown 里**根本不存在**（标题被压平成了
营销文案），而 TOC 里的锚点（#gemini-2-5-flash-image 等）没有留在正文 ⇒ 提取器
无法把任何一行归到某个模型。**修法在抓取那一步，不在列映射。

★ 第二张表里那两行带日期边界的价（`$0.75 through December 31, 2026. $1.50
starting January 1, 2027.`）是同一页上**另一个独立**的障碍：即使模型名
救回来了，**这一格也给不出唯一的基准价**。
★ 紧跟着的 `Cache read price | ... $0.075 $1.00 ... per hour` 是**合法**的
两笔金额（缓存读 + 存储），故意留在夹具里：用来证明新诊断不是
「一格有两个金额就报」。**
-->

## Paid

For production applications that require higher volumes and advanced features.

Our most intelligent model built for speed, combining frontier intelligence with
superior search and grounding.

|  | Free Tier | Paid Tier, per 1M tokens in USD |
| --- | --- | --- |
| Input price | Free of charge | $1.50 |
| Output price (including thinking tokens) | Free of charge | $9.00 |
| Context caching price | Free of charge | $0.15 $1.00 / 1,000,000 tokens per hour (storage price) |
| Grounding with Google Search* | Not available | 5,000 prompts per month (free, shared across Gemini 3), then $14 / 1,000 search queries |
| Grounding with Google Maps | Not available | 5,000 prompts per month (free, shared across Gemini 3), then $14 / 1,000 search queries |
| Used to improve our products | [Yes](https://ai.google.dev/gemini-api/terms) | [No](https://ai.google.dev/gemini-api/terms) |

Our low-latency, real-time speech to speech translation model that supports 70+
languages.

|  | Free Tier | Paid Tier, per 1M tokens in USD |
| --- | --- | --- |
| Input price | Not available | $0.75 through December 31, 2026. $1.50 starting January 1, 2027. |
| Output price (including thinking tokens) | Not available | $3.75 through December 31, 2026. $7.50 starting January 1, 2027. |
| Cache read price | Not available | $0.075 $1.00 / 1,000,000 tokens per hour (storage price) |
| Free tier | free through December 31, 2026 | free until December 31, 2026 |
| Output price (including thinking tokens) | Not available | $4.50 |
| Context caching price | Not available | $0.075 $1.00 / 1,000,000 tokens per hour (storage price) |

## Gemini 2.0 Flash

| Model | Input | Output |
| --- | --- | --- |
| Gemini 2.0 Flash | $0.10 | $0.40 |
