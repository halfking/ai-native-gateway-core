# Filter Options Page

Maintain the available options in filters on each list page (dropdown contents such as models, keys, and tenants).

## Definition

Option sources = values that have actually appeared + a manually maintained allowlist; combine both and remove duplicates.

## How to Self-Check When Numbers Do Not Match

- 1. If a list page filter cannot find a value: first check here to confirm whether the option is registered; dynamic values will appear automatically as data comes in.
- An option was deleted but reappeared: real traffic is generating that value, and deletion only affects the manually maintained portion.
