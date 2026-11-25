# Usage Cost Page

Usage analysis from a cost perspective: who is spending the most and where the money is going.

## Calculation Basis

Cost attribution and cache metrics use `request_metrics` for the last 30 calendar days in the system time zone. The charts show recorded USD request costs. Credits are tracked separately. Missing dimensions and long-tail groups are shown separately.

- Cache hit rate uses input tokens with a known accounting convention. OpenAI and Gemini include cache reads in input. Anthropic reports ordinary input, reads, and writes as separate counts. The page shows how many requests are covered.
- Blank input cost, output cost, and savings indicate missing billing evidence. Compression counts use requests with a recorded compression status.

## How to Self-Check When the Numbers Do Not Match

- Daily reconciliation uses closed calendar-day snapshots. This page refreshes with the projection and includes requests already projected for the current day.
- Include the “Other” and uncategorized groups when checking a breakdown against its total. USD totals use the currency recorded for each request.
