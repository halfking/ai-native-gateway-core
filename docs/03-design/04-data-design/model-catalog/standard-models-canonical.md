# 标准模型原厂配置表（Standard Models · Canonical Catalog）

> **Snapshot date**: 2026-08-29
> **Source-of-truth**: 各厂商原厂文档（Anthropic / OpenAI / Google AI Studio /
> DeepSeek / 智谱 / 阿里 / 月之暗面 / 字节 / xAI / Meta / Mistral / MiniMax 等）。
> **Refresh policy**: 季度复核；新模型发布时即时增量更新。
> **配对 SQL 快照**: [`standard-models-canonical.csv`](./standard-models-canonical.csv)
> / [`standard-models-canonical.json`](./standard-models-canonical.json)
> / [`standard-models-canonical.sql`](./standard-models-canonical.sql)
>
> **本目录定位**: 当 `models_canonical` 表因任何原因被破坏、需要按厂商
> 重新导入时，本目录提供的 `.sql` + `.csv` + `.json` 是唯一可信的
> "single source of truth"。DB 中 `source IN ('seed','seed-standard-rollout',
> 'db','manual','standard')` 的所有行都必须能在这里找到对应记录。

---

## 1. 阅读约定

| 列 | 含义 |
|---|---|
| `canonical_name` | 网关内统一标准名（小写、横线，参考 `modelname/` 包归一化） |
| `family` | 厂商族；路由按 family 分桶 |
| `context_window` | 厂商公布的输入上下文（tokens），**仅作基准值**；凭据×模型级覆盖见 `credential_model_bindings.context_window_override` |
| `modality` | 主模态：`text` / `vision` / `audio` / `video` / `multimodal` / `embedding` |
| `multimodal_caps` | 多模态细粒度标签（如 `{audio}` 表示除文本外还支持音频输入） |
| `parameters_b` | 参数量（十亿），缺失即厂商未公开 |
| `released_at` | 发布日期，缺失即历史模型/未公开 |
| `version_rank` | 版本级次：1=最新、2=次新、3+=稳定版，用于路由策略 |
| `complexity_ceiling` | 模型能稳定承接的最高任务难度（easy/medium/hard/frontier） |
| `source` | DB 行最初来源：`seed` / `seed-standard-rollout` / `db` / `manual` / `standard` |

---

## 2. 厂商索引（按 family 字典序）

| Family | 厂商 | 文档 URL | 验证日期 |
|---|---|---|---|
| `abab5.5` | MiniMax（旧名 minimax） | https://api.minimaxi.com | 2026-08-29 |
| `abab6.5s` | MiniMax | https://api.minimaxi.com | 2026-08-29 |
| `allamoe` | SDAIA（沙特国家 AI 局） | https://huggingface.co/ALLaMo-AI | 2026-08-29 |
| `anthropic-claude` | Anthropic | https://docs.anthropic.com/en/docs/about-claude/models/overview | 2026-08-29 |
| `baichuan` | 百川智能 | https://platform.baichuan-ai.com/docs | 2026-08-29 |
| `bigscience-bloom` | BigScience | https://huggingface.co/bigscience/bloom | 2026-08-29 |
| `cohere` | Cohere | https://docs.cohere.com/docs/models | 2026-08-29 |
| `cursor` | Cursor | https://cursor.com | 2026-08-29 |
| `deepseek` | 深度求索 | https://api-docs.deepseek.com | 2026-08-29 |
| `doubao` | 字节跳动（豆包/火山方舟） | https://www.volcengine.com/docs/82379 | 2026-08-29 |
| `eleutherai` | EleutherAI | https://huggingface.co/EleutherAI | 2026-08-29 |
| `ernie` | 百度（文心一言） | https://cloud.baidu.com/doc/WENXINWORKSHOP | 2026-08-29 |
| `falcon` | TII（Inception） | https://huggingface.co/tiiuae | 2026-08-29 |
| `gemma` | Google | https://ai.google.dev/gemma/docs | 2026-08-29 |
| `google-gemini` | Google Gemini API | https://ai.google.dev/gemini-api/docs/models | 2026-08-29 |
| `google-palm` | Google PaLM 2 | https://ai.google.dev/palm_docs | 2026-08-29 |
| `hunyuan` | 腾讯混元 | https://cloud.tencent.com/document/product/1729 | 2026-08-29 |
| `kuae` | 光年之外 / 昆仑万维 | https://www.skyworkapi.com | 2026-08-29 |
| `meta-llama` | Meta | https://llama.meta.com/docs | 2026-08-29 |
| `microsoft-phi` | Microsoft | https://learn.microsoft.com/azure/ai-studio/concepts/models | 2026-08-29 |
| `mimo` | 小米 MiMo | https://dev.mi.com/xiaomi-mimo | 2026-08-29 |
| `minimax` | MiniMax | https://api.minimaxi.com | 2026-08-29 |
| `mistral` | Mistral AI | https://docs.mistral.ai/getting-started/models | 2026-08-29 |
| `moonshot` | 月之暗面 | https://platform.moonshot.cn/docs | 2026-08-29 |
| `naver-hyperclova` | NAVER | https://clova-x.naver.com | 2026-08-29 |
| `nvidia-nemotron` | NVIDIA | https://docs.api.nvidia.com/nim/reference/llm-apis | 2026-08-29 |
| `openai-audio` | OpenAI | https://platform.openai.com/docs/models | 2026-08-29 |
| `openai-embedding` | OpenAI | https://platform.openai.com/docs/models | 2026-08-29 |
| `openai-gpt` | OpenAI | https://platform.openai.com/docs/models | 2026-08-29 |
| `openai-image` | OpenAI | https://platform.openai.com/docs/models | 2026-08-29 |
| `pangu` | 华为盘古 | https://support.huaweicloud.com/productdesc-pangu | 2026-08-29 |
| `perplexity` | Perplexity | https://docs.perplexity.ai | 2026-08-29 |
| `perplexity-sonar` | Perplexity Sonar | https://docs.perplexity.ai/guides/model-cards | 2026-08-29 |
| `qwen` / `qwen2` / `qwen3` / `qwq` | 阿里通义千问 | https://help.aliyun.com/zh/model-studio/developer-reference/model-overview | 2026-08-29 |
| `rinna` | Rinna | https://huggingface.co/rinna | 2026-08-29 |
| `sensetime` | 商汤科技（日日新） | https://platform.sensenova.cn/doc | 2026-08-29 |
| `spark` | 科大讯飞（星火） | https://www.xfyun.cn/doc/spark | 2026-08-29 |
| `stability` | Stability AI | https://platform.stability.ai/docs | 2026-08-29 |
| `stepfun` | 阶跃星辰 | https://platform.stepfun.com/docs | 2026-08-29 |
| `together` | Together AI | https://docs.together.ai/docs | 2026-08-29 |
| `xai` / `xai-grok` | xAI | https://docs.x.ai/docs/models | 2026-08-29 |
| `yi` | 零一万物 | https://platform.lingyiwanwu.com/docs | 2026-08-29 |
| `youdao` | 网易有道 | https://ai.youdao.com | 2026-08-29 |
| `zhipu-glm` | 智谱 AI | https://open.bigmodel.cn/dev/howuse/model-introduction | 2026-08-29 |

---

## 3. 完整模型表（按 family 分组）

> **图例**: 🎨 支持图像/视觉 · 🔊 支持音频 · 🎬 支持视频 · 🧠 支持 reasoning
> （tags 含 `reasoning` 的即为推理模型）

### 3.1 abab5.5（MiniMax 旧系列）

| canonical_name | display | ctx | modality | params | released | doc |
|---|---|---:|---|---:|---|---|
| `abab5.5-chat` | MiniMax ABAB 5.5 Chat | 16 384 | text | – | – | [api.minimaxi.com](https://api.minimaxi.com) |

### 3.2 abab6.5s（MiniMax ABAB 6.5s 系列）

| canonical_name | display | ctx | modality | params | released | doc |
|---|---|---:|---|---:|---|---|
| `abab6.5s-chat` | MiniMax ABAB 6.5s Chat | 245 760 | text | – | – | [api.minimaxi.com](https://api.minimaxi.com) |

### 3.3 allamoe（SDAIA ALLaMo）

| canonical_name | display | ctx | modality | params | released | doc |
|---|---|---:|---|---:|---|---|
| `allamoe-13b` | SDAIA ALLaMo-E 13B | – | text | 13B | – | [HF](https://huggingface.co/ALLaMo-AI) |

### 3.4 anthropic-claude（Anthropic Claude）🎨 全部支持视觉

| canonical_name | ctx | modality | version_rank | doc |
|---|---:|---|---:|---|
| `claude-3-5-haiku` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-3-5-sonnet` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-3-5-sonnet-20241022` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-3-7-sonnet` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-haiku-4.5` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-haiku-4.6` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-opus-4` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-opus-4.5` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-opus-4.6` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-opus-4.7` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-sonnet-4` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-sonnet-4.5` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |
| `claude-sonnet-4.6` | 200 000 | multimodal 🎨 | – | [Anthropic](https://docs.anthropic.com/en/docs/about-claude/models/overview) |

> 所有 Claude 3.x / 4.x 文本+视觉模型均为 200K tokens；Anthropic 官方不公开
> parameters_b / released_at / version_rank。

### 3.5 baichuan（百川智能）

| canonical_name | display | ctx | modality | params | released | doc |
|---|---|---:|---|---:|---|---|
| `baichuan-13b` | 百川 13B | – | text | 13B | – | [platform](https://platform.baichuan-ai.com/docs) |
| `baichuan-53b` | 百川 53B | – | text | 53B | – | [platform](https://platform.baichuan-ai.com/docs) |
| `baichuan3-turbo` | 百川 3 Turbo | 32 768 | text | – | – | [platform](https://platform.baichuan-ai.com/docs) |
| `baichuan3-turbo-128k` | 百川 3 Turbo 128K | 131 072 | text | – | – | [platform](https://platform.baichuan-ai.com/docs) |
| `baichuan4` | 百川 4 | 32 768 | text | – | – | [platform](https://platform.baichuan-ai.com/docs) |

### 3.6 bigscience-bloom

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `bloom-176b` | – | text | 176B | [HF](https://huggingface.co/bigscience/bloom) |

### 3.7 cohere

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `command-r` | Command R | 131 072 | text | [docs.cohere.com](https://docs.cohere.com/docs/models) |
| `command-r-plus` | Command R+ | 131 072 | text | [docs.cohere.com](https://docs.cohere.com/docs/models) |
| `embed-v3` | Embed v3 | – | text (embedding) | [docs.cohere.com](https://docs.cohere.com/docs/models) |
| `rerank-v3` | Rerank v3 | – | text (rerank) | [docs.cohere.com](https://docs.cohere.com/docs/models) |

### 3.8 cursor

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `composer` | – | text | [cursor.com](https://cursor.com) |
| `cursor-small` | – | text | [cursor.com](https://cursor.com) |

### 3.9 deepseek（深度求索）🧠 部分支持 reasoning

| canonical_name | ctx | modality | params | reasoning | doc |
|---|---:|---|---:|:---:|---|
| `deepseek-chat` | 65 536 | text | – | – | [api-docs](https://api-docs.deepseek.com) |
| `deepseek-coder` | 65 536 | text | – | – | [api-docs](https://api-docs.deepseek.com) |
| `deepseek-r1` 🧠 | 65 536 | text | 671B | ✅ | [api-docs](https://api-docs.deepseek.com) |
| `deepseek-v3` | 65 536 | text | 671B | – | [api-docs](https://api-docs.deepseek.com) |
| `deepseek-v3.1` | 65 536 | text | 671B | – | [api-docs](https://api-docs.deepseek.com) |

### 3.10 doubao（字节跳动 / 火山方舟）

| canonical_name | display | ctx | modality | complexity | doc |
|---|---|---:|---|:---:|---|
| `doubao-lite-4k` | 豆包 Lite 4K | 4 096 | text | easy | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-pro-4k` | 豆包 Pro 4K | 4 096 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-pro-32k` | 豆包 Pro 32K | 32 768 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-1-5-pro-32k` | 豆包 1.5 Pro 32K | 32 768 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-1-5-lite-32k` | 豆包 1.5 Lite 32K | 32 768 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-pro-128k` | 豆包 Pro 128K | 131 072 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-1-5-pro-256k` | 豆包 1.5 Pro 256K | 262 144 | text | medium | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-seed-2.0-pro` | 豆包 Seed 2.0 Pro | 131 072 | text | – | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-seed-2.0-mini` | 豆包 Seed 2.0 Mini | 131 072 | text | – | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-seed-2.0-lite` | 豆包 Seed 2.0 Lite | 131 072 | text | – | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-seed-2-0-code-preview-260215` | 豆包 Seed 2.0 Code Preview | 131 072 | text | – | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-embedding-large-text` | 豆包嵌入（大文本） | 8 192 | embedding | – | [volcengine](https://www.volcengine.com/docs/82379) |
| `doubao-embedding-vision` | 豆包嵌入（视觉） | 32 768 | embedding | – | [volcengine](https://www.volcengine.com/docs/82379) |

### 3.11 eleutherai

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `gpt-neox-20b` | – | text | 20B | [HF](https://huggingface.co/EleutherAI/gpt-neox-20b) |
| `pythia-12b` | – | text | 12B | [HF](https://huggingface.co/EleutherAI/pythia-12b) |

### 3.12 ernie（百度文心一言）

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `ernie-3.5-8k` | 文心一言 3.5 | 8 192 | text | [cloud.baidu](https://cloud.baidu.com/doc/WENXINWORKSHOP) |
| `ernie-4.0-turbo-128k` | 文心一言 4.0 Turbo | 131 072 | text | [cloud.baidu](https://cloud.baidu.com/doc/WENXINWORKSHOP) |

### 3.13 falcon

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `falcon-180b` | – | text | 180B | [HF](https://huggingface.co/tiiuae/falcon-180B) |

### 3.14 gemma（Google 开源）

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `gemma-2-27b` | – | text | 27B | [ai.google.dev/gemma](https://ai.google.dev/gemma/docs) |
| `gemma-7b` | – | text | 7B | [ai.google.dev/gemma](https://ai.google.dev/gemma/docs) |

### 3.15 google-gemini（Google Gemini API）🎨 全部支持视觉

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `gemini-1.5-flash` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-1.5-pro` | 2 097 152 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-2.0-flash` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-2.0-flash-exp` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-2.0-flash-lite` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-2.5-flash` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |
| `gemini-2.5-pro` | 1 048 576 | multimodal 🎨 | [ai.google.dev/gemini-api/docs/models](https://ai.google.dev/gemini-api/docs/models) |

### 3.16 google-palm

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `palm-2` | – | text | [ai.google.dev/palm_docs](https://ai.google.dev/palm_docs) |

### 3.17 hunyuan（腾讯混元）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `hunyuan-lite` | – | text | [cloud.tencent.com](https://cloud.tencent.com/document/product/1729) |
| `hunyuan-pro` | – | text | [cloud.tencent.com](https://cloud.tencent.com/document/product/1729) |
| `hunyuan-turbo` | – | text | [cloud.tencent.com](https://cloud.tencent.com/document/product/1729) |

### 3.18 kuae（光年之外 / 昆仑万维）

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `kuae-1.5` | 光年 1.5 | – | text | [skyworkapi](https://www.skyworkapi.com) |
| `skywork-13b` | 天工 13B | – | text | [skyworkapi](https://www.skyworkapi.com) |

### 3.19 meta-llama（Meta Llama）🎨 Vision 变体

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `codellama-34b` | – | text | 34B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-2-70b-chat` | – | text | 70B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3-70b` | – | text | 70B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3-8b` | – | text | 8B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.1-8b-instruct` | 131 072 | text | 8B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.1-70b-instruct` | 131 072 | text | 70B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.1-405b-instruct` | 131 072 | text | 405B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.2-3b-instruct` | 131 072 | text | 3B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.2-90b-vision-instruct` | 131 072 | multimodal 🎨 | 90B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-3.3-70b-instruct` | 131 072 | text | 70B | [llama.meta.com](https://llama.meta.com/docs) |
| `llama-4-preview` | 10 485 760 | multimodal 🎨 | – | [llama.meta.com](https://llama.meta.com/docs) |

> Llama 4 Preview 是 Scout 早期预览版，宣称 10M context；当前 release
> 后可能有变动，请在生产前再次复核。

### 3.20 microsoft-phi（Microsoft Phi）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `phi-3-medium` | – | text | [learn.microsoft.com](https://learn.microsoft.com/azure/ai-studio/concepts/models) |
| `phi-4` | – | text | [learn.microsoft.com](https://learn.microsoft.com/azure/ai-studio/concepts/models) |

### 3.21 mimo（小米 MiMo）🎨

| canonical_name | display | ctx | modality | params | doc |
|---|---|---:|---|---:|---|
| `mimo-v2.5-pro` | 小米 MiMo v2.5 Pro | 128 000 | multimodal 🎨 | 14B | [dev.mi.com](https://dev.mi.com/xiaomi-mimo) |

### 3.22 minimax（MiniMax）🎨 M3 支持视觉

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `minimax-m2.5` | 245 760 | text | [api.minimaxi.com](https://api.minimaxi.com) |
| `minimax-m2.7` | 245 760 | text | [api.minimaxi.com](https://api.minimaxi.com) |
| `minimax-text-01` | 1 000 000 | text | [api.minimaxi.com](https://api.minimaxi.com) |

> 注：另有 `minimax-m3` 标记为 multimodal 512K（在本表中以多模态能力披露）。
> 严格来说 M3 是 MiniMax 当前最强视觉模型，512K context。

### 3.23 mistral（Mistral AI）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `codestral` | 32 768 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `codestral-latest` | 32 768 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `ministral-8b` | 131 072 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `mistral-large-latest` | 131 072 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `mistral-small-latest` | 32 768 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `mixtral-8x22b` | 65 536 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |
| `open-mistral-nemo` | 131 072 | text | [docs.mistral.ai](https://docs.mistral.ai/getting-started/models) |

### 3.24 moonshot（月之暗面 / Kimi）🎨 K2.6 / K3 支持视觉

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `kimi-chat` | Kimi Chat | 8 192 | text | [platform.moonshot.cn](https://platform.moonshot.cn/docs) |
| `moonshot-v1-8k` | Moonshot v1 8K | 8 192 | text | [platform.moonshot.cn](https://platform.moonshot.cn/docs) |
| `moonshot-v1-32k` | Moonshot v1 32K | 32 768 | text | [platform.moonshot.cn](https://platform.moonshot.cn/docs) |
| `moonshot-v1-128k` | Moonshot v1 128K | 131 072 | text | [platform.moonshot.cn](https://platform.moonshot.cn/docs) |

> 注意：`kimi-k3` / `kimi-k2.6` / `kimi-k2.7-code(-highspeed)` 不在
> `seed-standard-rollout` 范围中（由 `migration-354` 写入），故未列于本节。
> 它们的权威值已在 `sql/migrations/startup/611_*.sql` 中固化。

### 3.25 naver-hyperclova

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `hyperclova-x` | – | text | [clova-x.naver.com](https://clova-x.naver.com) |

### 3.26 nvidia-nemotron

| canonical_name | ctx | modality | params | doc |
|---|---:|---|---:|---|
| `nemotron-4-340b` | – | text | 340B | [docs.api.nvidia.com](https://docs.api.nvidia.com/nim/reference/llm-apis) |

### 3.27 openai-audio（OpenAI 音频）🔊

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `tts-1` | – | text (audio out) | [platform.openai.com](https://platform.openai.com/docs/models) |
| `whisper-1` | – | text (audio in) | [platform.openai.com](https://platform.openai.com/docs/models) |

### 3.28 openai-embedding

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `text-embedding-3` | 8 192 | embedding | [platform.openai.com](https://platform.openai.com/docs/models) |

### 3.29 openai-gpt（OpenAI GPT / o 系列）🧠 o 系列支持 reasoning · 🎨 vision

| canonical_name | ctx | modality | reasoning | doc |
|---|---:|---|:---:|---|
| `gpt-3.5-turbo` | 16 385 | text | – | [platform.openai.com](https://platform.openai.com/docs/models) |
| `gpt-4-turbo` | 128 000 | multimodal 🎨 | – | [platform.openai.com](https://platform.openai.com/docs/models) |
| `gpt-4o` | 128 000 | multimodal 🎨 | – | [platform.openai.com](https://platform.openai.com/docs/models) |
| `gpt-4o-audio-preview` | 128 000 | multimodal (audio) 🔊🎨 | – | [platform.openai.com](https://platform.openai.com/docs/models) |
| `gpt-4o-mini` | 128 000 | multimodal 🎨 | – | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o1` 🧠 | 200 000 | multimodal 🎨 | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o1-mini` 🧠 | 128 000 | text | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o1-preview` 🧠 | – | text | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o3` 🧠 | 200 000 | multimodal 🎨 | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o3-mini` 🧠 | 200 000 | multimodal 🎨 | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o4-mini` 🧠 | 200 000 | multimodal 🎨 | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |
| `o5-preview` 🧠 | – | text | ✅ | [platform.openai.com](https://platform.openai.com/docs/models) |

### 3.30 openai-image

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `dall-e-3` | – | text (image out) | [platform.openai.com](https://platform.openai.com/docs/models) |

### 3.31 pangu（华为盘古）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `pangu-chat` | – | text | [support.huaweicloud](https://support.huaweicloud.com/productdesc-pangu) |

### 3.32 perplexity / perplexity-sonar 🧠

| canonical_name | ctx | modality | reasoning | doc |
|---|---:|---|:---:|---|
| `sonar` | 200 000 | text | – | [docs.perplexity.ai](https://docs.perplexity.ai) |
| `sonar-reasoning` | 200 000 | text | ✅ | [docs.perplexity.ai](https://docs.perplexity.ai) |
| `sonar-pro` | 200 000 | text | – | [docs.perplexity.ai/guides/model-cards](https://docs.perplexity.ai/guides/model-cards) |

### 3.33 qwen / qwen2 / qwen3 / qwq（阿里通义千问）🧠

| canonical_name | ctx | modality | params | reasoning | doc |
|---|---:|---|---:|:---:|---|
| `qwen-max` | 32 768 | text | – | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen-plus` | 131 072 | text | – | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen-turbo` | 131 072 | text | – | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen2.5-7b-instruct` | 131 072 | text | 7B | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen2.5-72b-instruct` | 131 072 | text | 72B | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen2-72b` | 32 768 | text | 72B | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwen3-235b` | 131 072 | text | 235B | – | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwq-32b` 🧠 | 131 072 | text | 32B | ✅ | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |
| `qwq-32b-preview` 🧠 | 131 072 | text | 32B | ✅ | [help.aliyun.com](https://help.aliyun.com/zh/model-studio/developer-reference/model-overview) |

### 3.34 rinna

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `rinna-3.6b` | – | text | [HF](https://huggingface.co/rinna) |

### 3.35 sensetime（商汤日日新）🧠

| canonical_name | display | ctx | modality | reasoning | doc |
|---|---|---:|---|:---:|---|
| `sensechat-5` | 日日新 SenseChat 5 | 131 072 | text | – | [platform.sensenova.cn](https://platform.sensenova.cn/doc) |
| `sensechat-5-thinking` | 日日新 SenseChat 5 Thinking | 131 072 | text | ✅ | [platform.sensenova.cn](https://platform.sensenova.cn/doc) |
| `sensechat-turbo` | 日日新 SenseChat Turbo | 32 768 | text | – | [platform.sensenova.cn](https://platform.sensenova.cn/doc) |
| `sensenova-xl` | 日日新 SenseNova XL | – | text | – | [platform.sensenova.cn](https://platform.sensenova.cn/doc) |

### 3.36 spark（科大讯飞星火）

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `spark-3.5` | 星火 3.5 | – | text | [xfyun.cn](https://www.xfyun.cn/doc/spark) |
| `spark-max` | 星火 Max | – | text | [xfyun.cn](https://www.xfyun.cn/doc/spark) |

### 3.37 stability（Stability AI）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `stable-diffusion-3` | – | text (image out) | [platform.stability.ai](https://platform.stability.ai/docs) |
| `stable-lm-2` | – | text | [platform.stability.ai](https://platform.stability.ai/docs) |

### 3.38 stepfun（阶跃星辰）🎨

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `step-1-256k` | Step-1 256K | 262 144 | text | [platform.stepfun.com](https://platform.stepfun.com/docs) |
| `step-1v-32k` | Step-1V 32K | 32 768 | multimodal 🎨 | [platform.stepfun.com](https://platform.stepfun.com/docs) |
| `step-2-16k` | Step-2 16K | 16 384 | text | [platform.stepfun.com](https://platform.stepfun.com/docs) |

### 3.39 together

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `together-7b` | – | text | [docs.together.ai](https://docs.together.ai/docs) |

### 3.40 xai / xai-grok（xAI Grok）🎨 Grok 全系支持视觉 · 🧠 推理

| canonical_name | display | ctx | modality | reasoning | doc |
|---|---|---:|---|:---:|---|
| `grok-1` | xAI Grok 1 | 131 072 | multimodal 🎨 | – | [docs.x.ai](https://docs.x.ai/docs/models) |
| `grok-2` | xAI Grok 2 | 131 072 | multimodal 🎨 | – | [docs.x.ai](https://docs.x.ai/docs/models) |
| `grok-2-1212` | xAI Grok 2 (12-12) | 131 072 | multimodal 🎨 | – | [docs.x.ai](https://docs.x.ai/docs/models) |
| `grok-3` | xAI Grok 3 | 131 072 | multimodal 🎨 | – | [docs.x.ai](https://docs.x.ai/docs/models) |
| `grok-3-mini` | xAI Grok 3 mini | 131 072 | multimodal 🎨 | ✅ | [docs.x.ai](https://docs.x.ai/docs/models) |

### 3.41 yi（零一万物）

| canonical_name | display | ctx | modality | doc |
|---|---|---:|---|---|
| `yi-large` | Yi Large | 32 768 | text | [platform.lingyiwanwu.com](https://platform.lingyiwanwu.com/docs) |
| `yi-large-turbo` | Yi Large Turbo | 16 384 | text | [platform.lingyiwanwu.com](https://platform.lingyiwanwu.com/docs) |
| `yi-medium` | Yi Medium | 16 384 | text | [platform.lingyiwanwu.com](https://platform.lingyiwanwu.com/docs) |

### 3.42 youdao（网易有道）

| canonical_name | ctx | modality | doc |
|---|---:|---|---|
| `youdao-translate` | – | text | [ai.youdao.com](https://ai.youdao.com) |

### 3.43 zhipu-glm（智谱 AI GLM）🎨 4V / 5V 支持视觉 · 🧠 Z1 / 5.3 推理

| canonical_name | display | ctx | modality | params | reasoning | complexity | doc |
|---|---|---:|---|---:|:---:|:---:|---|
| `chatglm-turbo` | ChatGLM Turbo | 131 072 | text | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `codegeex-4` | CodeGeeX 4 | 131 072 | text | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4` | GLM-4 | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4-9b-chat` | GLM-4 9B Chat | 131 072 | text | 9B | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4-air` | GLM-4 Air | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4-flash` | GLM-4 Flash | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4-plus` | GLM-4 Plus | 131 072 | text | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4.5` | GLM-4.5 | 131 072 | text | – | – | medium | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4.5-air` | GLM-4.5 Air | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4.5-flash` | GLM-4.5 Flash | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4.7` | GLM-4.7 | 131 072 | text | – | – | medium | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4.7-flash` | GLM-4.7 Flash | 131 072 | text | – | – | easy | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4v-flash` 🎨 | GLM-4V Flash | 8 192 | multimodal | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-4v-plus` 🎨 | GLM-4V Plus | 8 192 | multimodal | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-z1-flash` 🧠 | GLM-Z1 Flash | 65 536 | text | – | ✅ | medium | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-5` | GLM-5 | 131 072 | text | – | – | frontier | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-5.1` | GLM-5.1 | 131 072 | text | – | – | hard | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-5.2` | GLM-5.2 | 131 072 | text | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-5-2-260617` | GLM-5.2 (Volcengine 6月17日快照) | 131 072 | text | – | – | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |
| `glm-5.3` 🧠 | GLM-5.3 | 1 048 576 | text | – | ✅ | – | [open.bigmodel.cn](https://open.bigmodel.cn/dev/howuse/model-introduction) |

> GLM-5.x 系列实际上下文窗口 **131 072 tokens**（与 provider_catalog zhipu /
> nvidia / volcengine-coding 三家 manifest 全部一致，ctx_k=128）。

---

## 4. 验证记录（2026-08-29 快照）

| 字段 | 修复前 | 修复后 | 来源 |
|---|---|---|---|
| `grok-4.6` context_window | 500 | 262 144 | [x.ai/news/grok-4](https://x.ai/news/grok-4) |
| `kimi-k3` context_window | 1 000 | 1 048 576 | `modelname/modality_defaults.go:169` 注释 "1M context" |
| `kimi-k2.6` context_window | 256 | 262 144 | `modelname/modality_defaults.go:172` 注释 "256k context" |
| `kimi-k2.7-code` context_window | 256 | 262 144 | 配套 k2.6 |
| `kimi-k2.7-code-highspeed` context_window | NULL | 262 144 | 配套 k2.6 |
| `glm-5.3` context_window | 1 000 | 1 048 576 | `migration 354:12` alias notes "Z.AI GLM-5.3; 1M context" |
| `glm-5` context_window | 2 097 152 | 131 072 | `provider_catalog zhipu 'glm-5.1' ctx_k=128` |
| `glm-5.1` context_window | 2 097 152 | 131 072 | `provider_catalog zhipu/nvidia/volcengine-coding` 三家一致 |
| `glm-5.2` context_window | 128 000 | 131 072 | 标准化到 131072（与 zhipu/nvidia/volcengine 一致） |
| `grok-4.6` family | unknown | xai | 与 grok-2/3 对齐 |
| `kimi-k2.7-code-highspeed` family | kimi | moonshot | 与 kimi-k3/k2.6 对齐 |
| `o1/o3/o3-mini/o4-mini` modality | text | multimodal | OpenAI o 系列官方支持 vision image input |
| `gpt-4o-audio-preview` modality | audio | multimodal + caps=[audio] | 同时支持文本+图像+音频 |

总计：**80 行** `models_canonical` 被 611 迁移修正（`context_window_source='manual'`）。

---

## 5. 恢复流程

### 5.1 验证当前 DB 与本目录是否一致

```bash
# 重生成快照
bash sql/scripts/dump-standard-models.sh

# 与本目录比对
diff -u docs/03-design/04-data-design/model-catalog/standard-models-canonical.csv \
        sql/scripts/snapshots/standard_models_latest.csv
```

### 5.2 从本目录恢复

```bash
# 完整重建（覆盖现有 seed/db 来源的标准行）
psql "$LLM_GATEWAY_DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f docs/03-design/04-data-design/model-catalog/standard-models-canonical.sql
```

### 5.3 增量更新某厂商

```bash
# 例：仅刷新 Anthropic Claude
psql "$LLM_GATEWAY_DATABASE_URL" -v ON_ERROR_STOP=1 <<SQL
BEGIN;
UPDATE models_canonical SET
    context_window = 200000,
    modality       = 'multimodal',
    updated_at     = NOW()
WHERE family = 'anthropic-claude' AND status = 'active';
COMMIT;
SQL
```

---

## 6. 维护 / 复核 SOP

1. **季度复核**：每季度初对照各厂商文档，逐行复核 `context_window` /
   `modality` / `multimodal_caps` 是否与原文一致。
2. **新模型发布**：厂商发布 GA 模型后 7 个工作日内，将模型行加入
   `models_canonical` 并同步到本目录。
3. **弃用 / 下线**：将 `status` 改为 `deprecated` 或 `hidden`，并在本目录
   表格中以删除线或 `（已下线）` 标注。
4. **配对脚本**：`sql/scripts/dump-standard-models.sh` 必须可独立运行，CI
   流水线每月运行一次并将差异写入 PR 通知维护者。

---

## 7. 相关文件

| 文件 | 用途 |
|---|---|
| `standard-models-canonical.md` | 本文档（人读） |
| `standard-models-canonical.csv` | 机器可读快照（CSV） |
| `standard-models-canonical.json` | 机器可读快照（JSON） |
| `standard-models-canonical.sql` | 可执行的 INSERT … ON CONFLICT 脚本 |
| `sql/scripts/dump-standard-models.sh` | 重新生成快照的脚本 |
| `sql/migrations/startup/611_*.sql` | 修复迁移（变更历史） |
| `modelname/modality_defaults.go` | Go 端 modality 兜底表（与本文档对齐） |
| `modeliqdata/data/standard_iq.json` | AA Intelligence Index IQ 分（独立维度） |

