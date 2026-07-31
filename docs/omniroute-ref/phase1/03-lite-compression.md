# C1: Lite Compression

> Status: `RECONSTRUCTED-DRAFT`
> OmniRoute rule order is `SOURCE-VERIFIED`; Go integration is `NEW-DESIGN`.

## Source facts

`open-sse/services/compression/lite.ts` exposes a five-step Lite pipeline:

1. whitespace normalization;
2. system-message deduplication;
3. tool compression with the configured maximum tool length;
4. redundant-message removal;
5. image placeholder handling.

This is Lite only. OmniRoute also has strategy selection, engine registries, RTK, summarization, aggressive, and ultra paths in the compression service.

Go currently exposes `Compressor.Compress`, `Compressor.CompressAfter4xx`, and `CompressMessagesIfNeededBody`. The transformation package has separate `CompressMessagesIfNeeded` for OpenAI chat and `CompressAnthropicMessagesIfNeeded` for Anthropic Messages. There is no current `Compressor.Apply`; a unified Pipeline/Apply API is `NEW-DESIGN`.

## Proposed boundary

Translate the five rules as pure, fail-open stages with protected blocks and immutable input/output ownership. Integrate only after protocol-specific body preparation and coordinate with existing context-window trim, RecoveryCoordinator, Memora, and LLM summary paths. A request must not be compressed twice on retry.

## Gates

Golden JSON fixtures, protected code/URL tests, token/byte budget tests, and a default-off or explicit mode gate. A failed stage returns the original body and a structured reason; it must not convert a request failure into a malformed payload.
