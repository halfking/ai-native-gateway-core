# Provider List Page Data Notes

This page manages connected upstream “providers” (model services such as OpenAI and Claude), along with the credentials and models under each provider.

## Difference Between the Two Statuses

- **System health**: Credential-level availability (Normal/Warning/Unreachable/Unknown), based on probe results and failure records from actual traffic.
- **Routability**: Whether the provider currently **participates in traffic routing** (Available/Unavailable/No Models/Manually Disabled). A provider may be healthy but manually disabled—in that case, it does not receive traffic, but its health data is still updated.

## Relationship Between Models and Credentials

- A provider can have multiple credentials serving multiple models; an issue with any one credential only affects the models it supports.
- “No Models” = The provider is connected but has no available models configured, so it naturally does not receive traffic.

## Sorting and Filtering

- Providers are sorted by health status by default. The availability filter (Routable/Not Routable) is an independent switch. The two do not affect each other and only determine which rows are displayed.

## How to Self-Check When the Numbers Do Not Match

1. Healthy but no traffic: Check “Routability” first—manually disabled providers do not participate in traffic routing.
2. The model count does not match the actual available options: The model list is filtered by credential availability. When all credentials are offline, models temporarily disappear from the available options.
3. Newly saved credentials show “Probing”: After saving, the system automatically starts a health check and displays a transitional status until the result is available.
4. Filters are remembered after refreshing: The page retains the previous filter conditions. Clear the filters before someone else uses it.
