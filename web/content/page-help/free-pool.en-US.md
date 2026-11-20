# Free Resource Pool Page Data Notes

This page manages the “Zero-Cost Model Pool”: models from each provider’s official/community free tier are collected in one pool for use as a scheduling fallback.

## Three Sections

- **Free Models in the Pool**: The list of free models currently available (a model counts as “in the pool” only if it has usable credentials).
- **Template Catalog**: Known free providers and their expected models—“Connected” means the pool has active credentials for the provider; providers that are not connected are only “known to exist.”
- **Credential Management**: Registration (encrypted storage) and status of credentials dedicated to the free pool.

## Routing Priority

- Model routes already registered in the production catalog are **always prioritized over** the free pool; the free pool is used only as a fallback “when all production channels are unavailable.”
- Models in the pool are not covered by any SLA commitments: free tiers may be rate-limited or discontinued by upstream providers at any time.

## How to Self-Check When the Numbers Do Not Match

1. A model is listed in the template but not in the “in-pool list”: The template is an “expected list”; actual connections depend on credential status; the model enters the pool only after credentials are added.
2. Calls to in-pool models still fail: Free tiers are frequently rate-limited; failures are counted as credential errors, and models are automatically taken offline after consecutive failures.
3. “Free” usage generates a cost record: The request may have actually used a production channel after route fallback (the free pool was not matched); rely on the actual path shown in the request details.
