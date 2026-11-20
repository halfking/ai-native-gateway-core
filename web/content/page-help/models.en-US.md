# Model and Catalog Page Data Notes

This page maintains the **Model Dictionary**: which models the platform supports and how their canonical names correspond to each provider’s naming conventions.

## Three Tabs

- **Standard** (visible to platform operations): the platform’s approved list of canonical models—renaming, categorization, context length, and capability tags are all maintained here.
- **Catalog**: browse each provider’s original models and pricing.
- **Name Mapping**: different providers may use different names for the same model (such as names with version or date suffixes). The mapping table aligns each “provider name” with the “platform canonical name.” All statistical reports are aggregated by canonical name, so the more complete the mapping, the more accurate the reports.

## Why Canonical Names Matter

- All usage statistics, reconciliation, and leaderboards use the “canonical name” as the aggregation key.
- If a provider’s new model has not been mapped, its traffic appears separately under its original name (shown as “an extra unfamiliar model” in statistics). Once the mapping is added, it is automatically included under the canonical name.

## How to Self-Check When Numbers Do Not Match

1. If an unfamiliar model name appears in a report: first add a mapping under “Name Mapping,” then review the report again (historical data will be re-aggregated by canonical name).
2. Different prices for the same model across providers are normal: the Catalog lists prices separately by provider; costs in statistics are calculated using the price of the provider actually called.
3. If the context length does not match the official website: the platform Catalog configuration is the source of truth—it determines how the gateway actually performs truncation and compression.
