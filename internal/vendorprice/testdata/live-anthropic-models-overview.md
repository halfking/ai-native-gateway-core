Title: Models overview

URL Source: https://docs.anthropic.com/en/docs/about-claude/models/overview

Markdown Content:
Models & pricing Models

Claude is a family of state-of-the-art large language models developed by Anthropic. Compare the current lineup, find the model ID for every platform, and open each model's page for its full specs and resources.

## Compare models

If you're unsure which model to use, start with [Claude Opus 5.5](https://docs.anthropic.com/docs/en/models/opus-5-5/overview) for most workloads. Use [Claude Fable 5.1](https://docs.anthropic.com/docs/en/models/fable-5-1/overview) for demanding reasoning and long-horizon agentic work, or when your evals on Claude Opus 5.5 at higher effort still fall short. All current models support text and image input, text output, multilingual capabilities, vision, and tool use. Each model's page lists the platforms it's available on.

| Feature | [Claude Fable 5.1](https://docs.anthropic.com/docs/en/models/fable-5-1/overview)For demanding reasoning and long-horizon agentic work | [Claude Opus 5.5](https://docs.anthropic.com/docs/en/models/opus-5-5/overview)For long-running agentic coding and knowledge work | [Claude Sonnet 5.5](https://docs.anthropic.com/docs/en/models/sonnet-5-5/overview)The best combination of speed and intelligence | [Claude Haiku 4.5](https://docs.anthropic.com/docs/en/models/haiku-4-5/overview)The fastest model with near-frontier intelligence |
| --- | --- | --- | --- | --- |
|  | Slower | Moderate | Fast | Fastest |
|  | $10 / input MTok$50 / output MTok | $4 / input MTok$20 / output MTok | $2 / input MTok$10 / output MTok | $1 / input MTok$5 / output MTok |
|  |  |  |  |  |
| Capabilities |
|  | Adaptive (always on) | Adaptive (always on) | Adaptive | Extended |
|  | `high` | `medium` | `high` | Not supported |
|  | 1M tokens | 1M tokens | 1M tokens | 200K tokens |
|  | 128K tokens | 128K tokens | 128K tokens | 64K tokens |
|  | Jun 2026 | Jun 2026 | Jun 2026 | Feb 2025 |
|  |

Once you've picked a model, [learn how to make your first API call](https://docs.anthropic.com/docs/en/get-started). To understand how model IDs, aliases, and snapshots work, see [Model IDs and versioning](https://docs.anthropic.com/docs/en/about-claude/models/model-ids-and-versions); for the reliable-knowledge and training-data cutoffs behind each model, see [Anthropic's Transparency Hub](https://www.anthropic.com/transparency).

## Using the Models API

You can query model capabilities and token limits programmatically with the [Models API](https://docs.anthropic.com/docs/en/api/models/list). The response includes `max_input_tokens`, `max_tokens`, and a `capabilities` object for every available model.

## Prompt and output performance

Current Claude models excel in:

*   **Performance:** Top-tier results in reasoning, coding, multilingual tasks, long-context handling, honesty, and image processing. See [Prompting best practices](https://docs.anthropic.com/docs/en/build-with-claude/prompt-engineering/claude-prompting-best-practices) for general and model-specific prompting guidance.
*   **Engaging responses:** Claude models are ideal for applications that require rich, human-like interactions. If you prefer more concise responses, adjust your prompts to guide the model toward the desired output length. Refer to the [prompt engineering guides](https://docs.anthropic.com/docs/en/build-with-claude/prompt-engineering) for details.
*   **Output quality:** When migrating from a previous model generation, you may notice larger improvements in overall performance. If you're on Claude Opus 5 or earlier, see [Migrating to Claude Opus 5.5](https://docs.anthropic.com/docs/en/models/opus-5-5/migration-guide).

## Get started with Claude

If you're ready to start exploring what Claude can do for you, dive in! Whether you're a developer looking to integrate Claude into your applications or a user wanting to experience the power of AI firsthand, the following resources can help.

Explore Claude's capabilities and development flow.

Learn how to make your first API call in minutes.

Establish criteria and pick the right model for your use case.

Complete pricing, including batch discounts and prompt caching rates.

Lifecycle status and retirement commitments for every model.

Craft and test prompts directly in your browser.

Looking to chat with Claude? Visit [claude.ai](https://claude.ai/). If you have questions, reach out to the [support team](https://support.claude.com/) or the [Discord community](https://www.anthropic.com/discord).

Was this page helpful?
