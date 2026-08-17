Title: Pricing

URL Source: https://docs.x.ai/docs/pricing

Published Time: 2026-05-27T00:00:00Z

Markdown Content:
#### [Key Information](https://docs.x.ai/docs/pricing#key-information)

All prices are in USD. For per-model details, see the [models page](https://docs.x.ai/developers/models).

### Chat API

Prices per 1M tokens

| Model | Context | Input | Cached input | Output |
| --- | --- | --- | --- | --- |
| [grok-build-0.1](https://docs.x.ai/developers/models/grok-build-0.1) | 256k | $1.00 | $0.20 | $2.00 |
| [grok-4.3](https://docs.x.ai/developers/models/grok-4.3) | 1M | $1.25 | $0.20 | $2.50 |
| [grok-4.20-multi-agent-0309](https://docs.x.ai/developers/models/grok-4.20-multi-agent-0309) | 1M | $1.25 | $0.20 | $2.50 |
| [grok-4.20-0309-reasoning](https://docs.x.ai/developers/models/grok-4.20-0309-reasoning) | 1M | $1.25 | $0.20 | $2.50 |
| [grok-4.20-0309-non-reasoning](https://docs.x.ai/developers/models/grok-4.20-0309-non-reasoning) | 1M | $1.25 | $0.20 | $2.50 |

| Model | Media Input | Resolution | Output |
| --- | --- | --- | --- |
| [grok-imagine-image](https://docs.x.ai/developers/models/grok-imagine-image) Text, Image → Image | $0.002 / img | 1K | $0.02 / img |
| 2K | $0.02 / img |
| [grok-imagine-image-quality](https://docs.x.ai/developers/models/grok-imagine-image-quality) Text, Image → Image | $0.01 / img | 1K | $0.05 / img |
| 2K | $0.07 / img |
| [grok-imagine-video](https://docs.x.ai/developers/models/grok-imagine-video) Text, Image, Video → Video | $0.01 / sec$0.002 / img | 480p | $0.05 / sec |
| 720p | $0.07 / sec |
| [grok-imagine-video-1.5-preview](https://docs.x.ai/developers/models/grok-imagine-video-1.5-preview) Image → Video | $0.01 / img | 480p | $0.08 / sec |
| 720p | $0.14 / sec |

| Mode | Cost |
| --- | --- |
| [Realtime](https://docs.x.ai/developers/models/voice-agent-api) | $0.05 / min ($3.00 / hr) |
| [Realtime Text Input](https://docs.x.ai/developers/models/voice-agent-api) | $0.004 / message |
| [Text to Speech](https://docs.x.ai/developers/models/text-to-speech) | $15.00 / 1M chars |
| [Speech to Text](https://docs.x.ai/developers/models/speech-to-text) | $0.10 / hr (REST),$0.20 / hr (Streaming) |

## [Tools Pricing](https://docs.x.ai/docs/pricing#tools-pricing)

Requests which make use of xAI provided [server-side tools](https://docs.x.ai/developers/tools/overview) are priced based on two components: **token usage** and **server-side tool invocations**. Since the agent autonomously decides how many tools to call, costs scale with query complexity.

### [Token Costs](https://docs.x.ai/docs/pricing#token-costs)

All standard token types are billed for the model used in the request:

*   **Input tokens**: Your query and conversation history
*   **Reasoning tokens**: Agent's internal thinking and planning
*   **Completion tokens**: The final response
*   **Image tokens**: Visual content analysis (when applicable)
*   **Cached prompt tokens**: Prompt tokens that were served from cache rather than recomputed

### [Tool Invocation Costs](https://docs.x.ai/docs/pricing#tool-invocation-costs)

| Tool | Tool Name | Description | Cost / 1k Calls |
| --- | --- | --- | --- |
| [Web Search](https://docs.x.ai/developers/tools/web-search) | `web_search` | Search the internet and browse web pages | $5 / 1k calls |
| [X Search](https://docs.x.ai/developers/tools/x-search) | `x_search` | Search X posts, user profiles, and threads | $5 / 1k calls |
| [Code Execution](https://docs.x.ai/developers/tools/code-execution) | `code_execution``code_interpreter` | Run Python code in a sandboxed environment | $5 / 1k calls |
| [File Attachments](https://docs.x.ai/developers/files) | `attachment_search` | Search through files attached to messages | $10 / 1k calls |
| [Collections Search](https://docs.x.ai/developers/tools/collections-search) | `collections_search``file_search` | Query your uploaded document collections (RAG) | $2.50 / 1k calls |
| [Image Understanding](https://docs.x.ai/developers/tools/web-search#enable-image-understanding) | `view_image` | Analyze images found during Web Search and X Search* | Token-based |
| [X Video Understanding](https://docs.x.ai/developers/tools/x-search#enable-video-understanding) | `view_x_video` | Analyze videos found during X Search* | Token-based |
| [Remote MCP Tools](https://docs.x.ai/developers/tools/remote-mcp) | Tool name is set by each MCP server | Connect and use custom MCP tool servers | Token-based |

All tool names work in the Responses API. In the gRPC API (Python xAI SDK), `code_interpreter` and `file_search` are not supported.

*Only applies to images and videos found by search tools — not to images passed directly in messages.

For the view image and view x video tools, you will not be charged for the tool invocation itself but will be charged for the image tokens used to process the image or video.

Image Search is part of Web Search and is billed at the standard Web Search rate.

For Remote MCP tools, you will not be charged for the tool invocation but will be charged for any tokens used.

For more information on using Tools, please visit [our guide on Tools](https://docs.x.ai/developers/tools/overview).

* * *

## [Batch API Pricing](https://docs.x.ai/docs/pricing#batch-api-pricing)

The [Batch API](https://docs.x.ai/developers/advanced-api-usage/batch-api) lets you process large volumes of requests asynchronously at a fraction of the cost of standard pricing — effectively cutting your token costs in half. Batch requests are queued and processed in the background, with most completing within 24 hours.

|  | Real-time API | Batch API |
| --- | --- | --- |
| Token pricing | Standard rates | **20%-50% off** standard rates |
| Response time | Immediate (seconds) | Typically within 24 hours |
| Rate limits | Per-minute limits apply | Requests don't count towards rate limits |

The batch discount applies to all token types — input tokens, output tokens, cached tokens, and reasoning tokens. To see batch pricing for a specific model, visit the model's detail page and toggle **"Show batch API pricing"**.

The batch discount applies to text and language models only. Image and video generation are supported in the Batch API but are billed at standard rates. See [Batch API documentation](https://docs.x.ai/developers/advanced-api-usage/batch-api) for full details.

* * *

## [Files and Collections Pricing](https://docs.x.ai/docs/pricing#files-and-collections-pricing)

Files and collections stored on the xAI platform are billed based on the amount of storage used.

| Resource | Rate |
| --- | --- |
| File storage | $0.025 / GiB / day |
| Collection storage | $0.10 / GiB / day |

### [Download Costs](https://docs.x.ai/docs/pricing#download-costs)

Downloading data from files and collections is charged at a flat rate based on the amount of data transferred:

| Resource | Rate |
| --- | --- |
| File downloads | $0.20 / GiB downloaded |
| Collection downloads | $0.20 / GiB downloaded |

You can view and manage your [files](https://console.x.ai/team/default/files) and [collections](https://console.x.ai/team/default/collections) through the xAI console or the [xAI API](https://docs.x.ai/developers/files/managing-files).

* * *

## [Usage Guidelines Violation Fee](https://docs.x.ai/docs/pricing#usage-guidelines-violation-fee)

When your request is deemed to be in violation of our usage guideline by our system, we will still charge for the generation of the request.

For violations that are caught before generation in the Responses API, we will charge a $0.05 usage guideline violation fee per request.

* * *

## [Billing and Availability](https://docs.x.ai/docs/pricing#billing-and-availability)

Your model access might vary depending on various factors such as geographical location, account limitations, etc.

For how the **bills are charged**, visit [Manage Billing](https://docs.x.ai/console/billing) for more information.

For the most up-to-date information on **your team's model availability**, visit [Models Page](https://console.x.ai/team/default/models) on xAI Console.
