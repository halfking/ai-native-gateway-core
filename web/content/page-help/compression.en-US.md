# Compression Management Page Data Guide

This page monitors “slimming down overly long conversations”: how the system compresses a conversation when it is too long to fit within the model’s capacity.

## Key Metrics

- **Compressed Sessions**: The number of sessions that triggered compression.
- **Compression Ratio**: The percentage of content saved after compression.
- **Strategy Distribution**: The number of times each compression strategy was used (predictive/reactive/sliding window/incremental, etc.).
- **Compression Ratio Trend**: Changes in the daily average compression ratio.

## Difference Between the Two Trigger Methods

- **Predictive Compression** (initial): Compresses in advance when the conversation is estimated to be close to the capacity limit, with no impact on the user.
- **Forced Compression**: Compresses and retries only after the upstream first rejects the request (with a capacity error)—this costs one additional failed call, but compression only happens when necessary.

## Metric Definitions

- Compression happens “before the content is sent to the model,” so the original conversation is unaffected; the metric tracks the percentage reduction in the amount of content sent upstream.
- A single conversation round can trigger compression multiple times (repeatedly slimming down the content as it gets longer), with each occurrence counted separately.

## How to Reconcile Discrepancies in the Numbers

1. The compression ratio is unusually high, near 100%: Check whether the compression threshold was configured incorrectly (causing even very short questions to be compressed).
2. It is normal for the “Compressed” count to be much higher than the “Forced Compression” count: Most sessions are handled through predictive compression, and only those that were missed require forced compression.
3. If the configuration shows “Factory Default”: The top of the page will report that the configuration could not be read, meaning the values shown below are not the current actual configuration—resolve the read failure before adjusting any settings.
