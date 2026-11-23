## Cross-report metrics and reconciliation

Match the time range and time zone, tenant, traffic scope, counting unit and data freshness before comparing reports. Standard ranges include the start and exclude the end. The page notes above describe the current report. Some reports still use different windows or sources.

| Metric | Standard calculation |
| --- | --- |
| Client requests | Count each recorded client request at the gateway once. Completed requests include success, failure, rate limiting and terminal cancellation. Track in-progress requests separately. Retries and provider switches keep the original request count. |
| Provider sends | Count each call sent to a provider, including retries, parallel attempts and platform automation. A gateway rejection before sending counts only as a client request. Current daily snapshots summarize terminal gateway records. Full per-attempt coverage is pending. |
| Active and new sessions | Count distinct sessions with client requests in the window as active. Count a session as new if its first request falls in the window. Requests without a session remain in request totals. |
| Success rate | Successes divided by completed events, calculated separately for client requests and provider calls. A zero denominator means no data. |
| Input, output and total tokens | Sum normalized input and output separately. Total tokens equal input plus output. Show cache and reasoning details separately to avoid counting them twice. |
| Cached tokens and cache hit rates | Sum cache reads and writes separately. Token hit rate divides cache-read tokens by full input tokens, including cached input, with comparable cache data. Request hit rate divides hits by requests with comparable cache data. Show the denominator and coverage. |
| Estimated provider cost | Calculate each call using billable usage, the provider cost rates effective at the time and their currency. Track missing rates or usage separately. |
| Provider-reported amount | Record the original amount and currency when the response explicitly supplies them. Use the provider invoice to reconcile the final payable amount. |
| Internal credits and amount | Sum posted charges, reversals and adjustments. Preserve the unit and applicable conversion rules. Display credits and currency separately. |
| Platform automation | Assign probes and self-checks to a dedicated client. Include them in provider cost and platform usage and exclude them from tenant charges. The page notes show the current traffic scope. |

Zero represents a known value. Label missing usage or prices, unassigned items, unposted charges and delayed data. Compare amounts at the same currency and precision.
