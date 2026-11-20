# Masking Rules Page Data Guide

This page manages the rule library for the “Sensitive Information Interceptor”: which patterns in content sent to the model should be replaced or masked (phone numbers, ID numbers, key strings, etc.).

## Two Tabs

- **Rules**: A list of rules, each defining how to identify and replace a sensitive pattern.
- **Matches and Reviews**: Actual interception records showing which rule matched which location in which request and what the content was replaced with.

## How Rules Take Effect

- Each rule can be enabled or disabled separately by “Run Mode.” The global “Enabled” switch is the master switch—when it is off, no rules take effect.
- Rule changes **take effect immediately** for subsequent requests; historical requests that have already been sent are not reprocessed.

## How Match Records Are Counted

- A single request can match multiple rules, and each match is recorded separately.
- “Review” is the manual confirmation step: confirm whether a replacement was appropriate or a false positive. Reviews help continuously refine the rules and do not affect completed requests.

## How to Self-Check When Numbers Do Not Match

1. Sensitive information was clearly present but was not blocked: First check the master switch and the rule switch for the relevant mode, then confirm that the rule pattern matches the actual content (some rules recognize only specific formats).
2. The number of matches is higher than expected: Multiple matches within the same content are counted separately. Use the “Request” dimension to view the deduplicated request count.
3. There are too many false positives: In “Matches and Reviews,” group by rule to identify which rule is producing the most false positives, then adjust that rule’s matching conditions instead of turning off the master switch globally.
