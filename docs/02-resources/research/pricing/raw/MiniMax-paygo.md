Title: Pay as You Go - MiniMax API Docs

URL Source: https://platform.minimax.io/docs/guides/pricing-paygo

Markdown Content:
API Pricing

MiniMax Pay as You Go Pricing

Pay-as-you-go uses standard Open Platform API Keys and consumes your account balance by actual usage. Credits are a separate prepaid balance used through a Subscription Key with the same resource coverage as Token Plan. For Credits pricing and usage behavior, see [Token Plan pricing](https://platform.minimax.io/docs/guides/pricing-token-plan).

## LLM

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

*   Standard

*   Priority*

| Model | Input | Output | Prompt caching Read |
| --- | --- | --- | --- |
| **MiniMax-M3** ≤ 512k input tokens Permanent 50% off | ~~$0.60~~ $0.30 / M tokens | ~~$2.40~~ $1.20 / M tokens | ~~$0.12~~ $0.06 / M tokens |
| **MiniMax-M3** > 512k input tokens* Permanent 50% off | ~~$1.20~~ $0.60 / M tokens | ~~$4.80~~ $2.40 / M tokens | ~~$0.24~~ $0.12 / M tokens |

* Input tokens (including cache hits) above 512k are available in limited quantity for a limited time. Contact sales for access. Public availability is expected in the next few days.

| Model | Input | Output | Prompt caching Read |
| --- | --- | --- | --- |
| **MiniMax-M3** ≤ 512k input tokens Permanent 50% off | ~~$0.90~~ $0.45 / M tokens | ~~$3.60~~ $1.80 / M tokens | ~~$0.18~~ $0.09 / M tokens |
| **MiniMax-M3** > 512k input tokens Permanent 50% off | ~~$1.80~~ $0.90 / M tokens | ~~$7.20~~ $3.60 / M tokens | ~~$0.36~~ $0.18 / M tokens |

* Priority provides priority admission for faster response times and improved request reliability. Set `service_tier` to `priority` to enable it. Pricing is 1.5x standard.

| Model | Input | Output | Prompt caching Read | Prompt caching Write |
| --- | --- | --- | --- | --- |
| **MiniMax-M2.7** | $0.3 / M tokens | $1.2 / M tokens | $0.06 / M tokens | $0.375 / M tokens |
| **MiniMax-M2.7-highspeed** | $0.6 / M tokens | $2.4 / M tokens | $0.06 / M tokens | $0.375 / M tokens |

Legacy Models

| Model | Input | Output | Prompt caching Read | Prompt caching Write |
| --- | --- | --- | --- | --- |
| **MiniMax-M2.5** | $0.3 / M tokens | $1.2 / M tokens | $0.03 / M tokens | $0.375 / M tokens |
| **MiniMax-M2.5-highspeed** | $0.6 / M tokens | $2.4 / M tokens | $0.03 / M tokens | $0.375 / M tokens |
| **MiniMax-M2.1** | $0.3 / M tokens | $1.2 / M tokens | $0.03 / M tokens | $0.375 / M tokens |
| **MiniMax-M2.1-highspeed** | $0.6 / M tokens | $2.4 / M tokens | $0.03 / M tokens | $0.375 / M tokens |
| **MiniMax-M2** | $0.3 / M tokens | $1.2 / M tokens | $0.03 / M tokens | $0.375 / M tokens |

## Audio

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

| API | Model | Price |
| --- | --- | --- |
| **T2A** | speech-2.8-turbo | $60/M characters |
| **T2A** | speech-2.8-hd | $100/M characters |
| **Rapid Voice Cloning** | All Models | $1.5 per voice |
| **Voice Design** | All Models | $3 per voice |

Legacy Models

| API | Model | Price |
| --- | --- | --- |
| **T2A** | speech-2.6-turbo / speech-02-turbo | $60/M characters |
| **T2A** | speech-2.6-hd / speech-02-hd | $100/M characters |

## Video

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

| Model | Price |
| --- | --- |
| MiniMax-Hailuo-2.3-Fast | $0.19 per 768P, 6s video |
| MiniMax-Hailuo-2.3-Fast | $0.32 per 768P, 10s video |
| MiniMax-Hailuo-2.3-Fast | $0.33 per 1080P, 6s video |
| MiniMax-Hailuo-2.3 | $0.28 per 768P, 6s video |
| MiniMax-Hailuo-2.3 | $0.56 per 768P, 10s video |
| MiniMax-Hailuo-2.3 | $0.49 per 1080P, 6s video |

Legacy Models

| Model | Price |
| --- | --- |
| MiniMax-Hailuo-02 | $0.28 per 768P, 6s video |
| MiniMax-Hailuo-02 | $0.56 per 768P, 10s video |
| MiniMax-Hailuo-02 | $0.49 per 1080P, 6s video |
| MiniMax-Hailuo-02 | $0.10 per 512P, 6s video |
| MiniMax-Hailuo-02 | $0.15 per 512P, 10s video |

## Music

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

| Model | Price |
| --- | --- |
| Music-2.6 | $0.15/up-to-5 minutes music (Limited Free) |
| Lyrics Generation | $0.01/per song (Limited Free) |

Legacy Models

| Model | Price |
| --- | --- |
| Music-2.5+ | $0.15/up-to-5 minutes music |
| Music-2.5 | $0.15/up-to-5 minutes music |
| Music-2.0 | $0.03/up-to-5 minutes music |

## Image

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

| Model | Price |
| --- | --- |
| image-01 | $0.0035 per image |

## MCP

[Recharge Now](https://platform.minimax.io/user-center/payment/balance)

| Model | Input Price |
| --- | --- |
| **API-vlm** | $0.06 / request |

When API-vlm is called through Token Plan, usage deducts from the included Token Plan quota according to its pay-as-you-go price. If the included quota is exhausted and purchased Credits are available, additional usage can be automatically covered by purchased Credits.

[Overview](https://platform.minimax.io/docs/pricing/overview)[Audio Subscription](https://platform.minimax.io/docs/guides/pricing-speech)
