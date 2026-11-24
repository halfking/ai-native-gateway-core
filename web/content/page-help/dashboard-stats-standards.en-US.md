# Calculation Standards for the Board and Session Statistics

The "Board", "Session & Statistics" and "Provider Reconciliation" are three summaries of the same stream of model calls under **three different calculation rules**. This page spells out how every number is produced, where its data comes from and which time window it covers, and gives an **item-by-item breakdown** of why the three modules' numbers do not match — for aligning data and tracing deviations.

> Measured basis: 2026-11-23 21:05 (Asia/Shanghai) in production. All numbers were taken at the **same instant** (completed within a single SQL statement), window = `NOW()-7 days`; the provider reconciliation side was measured separately over the closed calendar-day range `2026-11-17 ~ 2026-11-23` and cross-checked against the fact table day by day.
>
> ★ Correction (2026-11-23 date-range alignment round): the Session & Statistics tab now uses **the same** date range as the Board tab (same control, same calendar-day range) ⇒ the Session & Statistics figures below were measured with the **pre-alignment** (rolling) window and have to be re-measured on the new window; the Board and reconciliation figures are unaffected.
> ★★ Correction (2026-11-24 truth-source unification, round 2): ★★ the Board's source of truth moved from the request-level fact table `usage_facts` **to the request-level logs `request_logs_with_current_month` as a whole** — the hero cards, the usage trend chart and the pies now read **the same view, the same terminal-status set and the same probe predicate** ⇒ one universe for the whole page, and long windows (last 30 days) no longer split the page in two (the card and the pies used to differ by 2×). ⇒ **the algorithms and source descriptions on this page have been corrected to the new caliber**; the **measured board-side figures below are still the 2026-11-23 measurement (pre-switch caliber)** and have to be re-measured on the new caliber before they are replaced (tracked as a to-do). The provider reconciliation side reads `report_snapshots` / `usage_facts` and is therefore **no longer guaranteed to match the board digit for digit** — see limitation 7 at the end of this page.

## How the three modules relate

| | Board | Session & Statistics | Provider Reconciliation |
| --- | --- | --- | --- |
| Statistical entity | One model call counts as one row (**request level**) | One session counts as one row, carrying that session's **cumulative values** (**session level**) | One model call counts as one row (**request level**, aggregated by provider) |
| Source of truth | Request-level logs `request_logs_with_current_month` | Session summary table `session_summaries` + the session universe | Daily snapshot table `report_snapshots` |
| Time window | Calendar days in the system time zone: midnight of today-(N-1) → now | **Same range as the Board** (calendar days in the system time zone: midnight of today-(N-1) → end of the last day; custom start/end supported) | **Closed calendar-day range** (whole day, including the full last day) |
| Probe traffic | Excluded by the probe markers carried by the request-level logs (switchable) | Excluded by the **session universe** rules | **Not excluded** (all traffic, including unassigned) |
| Maturity | Real time | Real time | **T+1 snapshot** (a date whose day has not ended yet lags behind) |

**Bottom line first**: the three modules' numbers **are not supposed to be equal** — different entities, different windows, different probe-exclusion rules, different maturity. Any cross-module comparison must align those four first; you cannot simply subtract one from the other.

### The most important one: probe traffic is almost free

Probe (health check / self-check) traffic is listed separately in the fact table as `traffic_class='probe'`:

| Caliber (same window) | Requests | Cost |
| --- | --- | --- |
| Excluding probes | 81,265 | $8,854.70 |
| Including probes | 189,534 | $8,856.87 |
| **Probe part** | **108,271** | **$2.17** |

⇒ **The request counts differ by a factor of 2.3, yet the cost differs by only $2.17.** Whenever you see "the request counts differ a lot but the cost is roughly the same", suspect the probe caliber first — not a miscalculation.

## Board: how each number is produced

| Card / module | Calculation | Notes |
| --- | --- | --- |
| Total requests | Count of terminal request rows in the window | Only terminal records (success / failure / rate limited); in-flight streams are excluded |
| Success rate | Successful rows ÷ total rows | Share of requests that returned a result |
| Total tokens | Sum of per-request input tokens + output tokens | Cached tokens follow each upstream's own accounting |
| Total cost | Sum of per-request cost (US dollars) | USD is the cost paid to upstream providers |
| Total credits | Credits charged by the platform to tenants | ★ See "The credits card" below |
| Average latency | Sum of per-request latency ÷ total rows | Milliseconds |
| Active models / providers | Distinct count of models / providers that actually carried traffic | Not the "total available on the platform" |
| Active keys | Number of keys currently enabled | **Configuration-based; independent of the time range** |
| Error count | Sum of each failure category in the error distribution; when that is unavailable, estimated as total requests × (1 − success rate) | The estimate can differ from the real value by a few rows |
| Pie charts (model / provider / tenant / client…) | Requests / tokens / cost grouped by dimension | From the request-level log — the same source as the cards above (same probe scope) |
| Usage trend chart | Requests / tokens / cost / credits bucketed over time | **Same source as the cards above**: the same request-level logs `request_logs_with_current_month`, the same probe predicate and the same terminal-status set ⇒ the buckets add up to the card |

### How the time window is computed

- "Today" = midnight today (system time zone, default Asia/Shanghai) → now; "Last 7 days" = midnight 6 days ago → now; "Last 30 days" likewise. **The start is a calendar-day midnight, the end is the current moment** — not a whole day.
- A custom range aggregates the two selected days, including the entire last day.
- The page refreshes every 10 seconds; when the live channel is up, newly completed requests are added immediately.

### The credits card (a special rule — please note)

The credits card does **not** show credits consumed inside the selected window: the credits column of the request-level fact table is currently always 0 (measured: all 298,037 rows are 0), so what the card shows is the **live delta accumulated after the cached baseline**. Therefore:

- Switching between "Today / Last 7 days / Last 30 days" leaves the credits card **exactly unchanged** (measured: all three show 86,925).
- The usage trend chart and the credits card on the same page are now **from the same source** (both read the request-level logs `request_logs_with_current_month`) ⇒ the buckets' credits add up to the card.

For a trustworthy credit ledger, use the "Usage & Cost" and "Reconciliation" pages instead; do not use the board credits card for time series comparisons.

## Session & Statistics: how each number is produced

| Module | Calculation | Notes |
| --- | --- | --- |
| Total sessions | Sessions whose first request falls in the window | Session-level count; not a request count |
| Active / new / closed sessions | Classified by whether the session's last / first request falls within the last 24 hours | A 24-hour rule, independent of the main window |
| Total cost (USD) | Sum of each session's cumulative cost | A session's **entire** cost is recorded on the session row |
| Total requests | Sum of each session's cumulative request count | Also the session's cumulative value, not requests that happened inside the window |
| Input cost / output cost | Sum of each session's input / output cost | The two add up to the total cost |
| Average cost per session / per request | Total cost ÷ sessions (÷ requests) | The denominator is the session-cumulative value |
| Success rate / cache rate (aligned row) | Successful rows ÷ total rows from the request-level view | Only the request-level source has these two |
| Session-analytics aligned row | Materialized-view session-level aggregate | Refreshed every 15 minutes; window = whole Shanghai calendar days |
| Top 5 users / Top 5 providers | Session-analytics ranking endpoints (users / models / providers / tasks) | The provider is aggregated by SQL over `model_usage_aggregates.provider` (not derived from the model-name prefix); **cost is included** and cost sorting is supported; the four Top 5 dimensions render on Session Analytics → Users |
| Turns cross-check | Session request count vs turns count | When the gap exceeds 10% the page shows a notice |

### The session universe (which sessions are counted)

A session is counted only when all three hold:

1. It is registered in the session table;
2. It has an owner (taken from the user key or the API-key owner key; if both are empty it is not counted);
3. It is not a probe session: neither manually flagged as a probe, nor matched as a self-check / deployment-gate / test / verification session by the "owner / client type / title" pattern.

## Full breakdown of the cost gap (measured 2026-11-23 at the same instant)

The Board's 81,265 requests / $8,854.70 (excluding probes), broken down **item by item into every block of the session statistics** (the six items below add up to the board total, with the cost closing to the cent):

| # | Component | Requests | Cost (USD) | Attribution |
| --- | --- | --- | --- | --- |
| C | Sessions **retained** inside the session universe | 58,180 | 1,551.41 | Session statistics (this is what the page shows) |
| D1 | Excluded · **manually flagged probes** | 12,364 | 5,145.16 | Session statistics gap |
| D2 | Excluded · **pattern-detected probes** | 5,946 | 1,876.67 | Session statistics gap |
| D3 | Excluded · **no owner** | 1,202 | 0.08 | Session statistics gap |
| E | **Sessions spanning the window** (first request outside, still active inside) | 3,191 | 281.38 | Session statistics gap |
| F | **No session affiliation** (direct API calls, scripted calls) | 381 | 0.00 | Board only |
| | **Total** | **81,263** | **8,854.70** | |

⇒ **The gap between session statistics and the board is mainly not "sessions spanning the window" but the session universe's probe exclusion rules (D1+D2).**

### Key measurement: everything that gets excluded is real business traffic

Classifying the in-window requests of the D1/D2/D3 excluded sessions by the request log's `origin_stage`:

| Exclusion reason | Requests | Cost (USD) | Of which `origin_stage='business'` | Share |
| --- | --- | --- | --- | --- |
| Manually flagged probes | 12,364 | 5,145.16 | 12,364 | **100%** |
| Pattern-detected probes | 5,946 | 1,876.67 | 5,946 | **100%** |
| No owner | 1,202 | 0.08 | 1,202 | **100%** |

⇒ The 19,512 requests and $7,021.91 of cost excluded on the session side are **100% business traffic (`origin_stage='business'`) in the request log** — not one of them is a probe. **This is the primary reason session statistics sit below the board, and it is the largest single deviation at present.**

## Number comparison across the three modules (measured 2026-11-23)

| Metric | Board (excluding probes) | Session & Statistics (panel) | Provider Reconciliation |
| --- | --- | --- | --- |
| Statistical entity | requests | sessions + session-cumulative requests | requests (aggregated by provider) |
| Requests | 81,265 | 58,180 (inside the universe) / 77,687 (full total outside the universe) | 178,841 (including unassigned) |
| Cost (USD) | 8,854.70 | 1,551.41 (inside the universe) | 8,612.07 |
| Probe traffic | already excluded | excluded by the session rules | **not excluded** |
| Maturity | real time | real time | **T+1 snapshot** |

### Board vs Provider Reconciliation: day-by-day cross-check

The reconciliation side reads `daily_total` from `report_snapshots` and cross-checks it against the fact table day by day (closed calendar-day range 11/17 ~ 11/23):

| Date | Fact-table requests | Reconciliation snapshot requests | Difference | Cost difference |
| --- | --- | --- | --- | --- |
| 11/17 | 27,446 | 27,446 | 0 | 0.0000 |
| 11/18 | 22,905 | 22,905 | 0 | 0.0000 |
| 11/19 | 35,008 | 35,008 | 0 | 0.0000 |
| 11/20 | 23,947 | 23,947 | 0 | 0.0000 |
| 11/21 | 21,341 | 21,341 | 0 | 0.0000 |
| 11/22 | 19,693 | 19,693 | 0 | 0.0000 |
| 11/23 | 36,019 | 28,501 | 7,518 | 42.38 |

⇒ **The first 6 days match request by request and cent by cent**; the difference on 11/23 comes 100% from the cutoff time of that day's snapshot (snapshot `updated_at` = 17:38, measurement time 21:05) — **it is not a calculation error, it is the normal lag of a day that has not ended yet**.

### Inside the reconciliation module: zero double counting

The provider rows of the reconciliation page (including "Unassigned") **sum to exactly the page total**:

- Provider rows sum = 178,841 requests = the page's total request count ✓
- Provider cost rows sum = $8,612.07 = the page's total cost ✓
- Of these, "Unassigned" (`provider_id IS NULL`) = 79,801 requests / 100% failed / $0.00 — these are **probe traffic without provider attribution** (probes are not sent to upstream providers), not a missing count.

## What to check when the numbers do not match

1. **Align the window first**: Board and Session & Statistics panel = **the same calendar-day range** (midnight of today-(N-1); the board counts up to now, the panel up to the end of the last day ⇒ both cover the same data, and both tabs use the same date-range control); aligned row = whole Shanghai calendar days; provider reconciliation = closed calendar-day range. The last two are still not the same window as the first two.
2. **Then align the entity**: compare requests with requests and cost with cost; never compare a session count with a request count.
3. **Then align the probe caliber**: the board can switch to "include probes"; session statistics excludes by the session universe rules; **the reconciliation always includes probes**. Request counts differing a lot while the costs are close ⇒ look at probes first.
4. **Then align the maturity**: the reconciliation is a T+1 snapshot, so **the current day's data necessarily lags**; confirm which day the snapshot covers before comparing.
5. **Look for degradation markers**: when a card shows "degraded / untrustworthy / not computed", those zeros do not mean "no traffic".
6. **The board trend chart and the board cards are now the same source**: both read the request-level logs `request_logs_with_current_month` with the same probe predicate ⇒ the buckets should add up to the card. If they do not, first check the `degraded` flag in the payload (the probe scope differs when it falls back to the pre-aggregated tables), then check that the time windows match.
7. **"Today" used to fail (fixed 2026-11-23)**: the "Today" preset of Session & Statistics reproducibly hit the 30-second timeout because the join index was missing on the database side (index added 2026-11-23: the query went from 121.8 s to 0.19 s). If the page shows that error again, the index has gone missing again — ask the on-call engineer to check that `idx_session_dim_session_key` is in place.

## Known limitations (measured 2026-11-23)

1. **The session universe excludes probes too broadly (D1/D2, confirmed by measurement on 2026-11-23)**: the 19,512 requests and $7,021.91 of cost excluded by session statistics are **100% business traffic** in the request log. Session statistics therefore sit about **$7,021.91 (79%)** below the board (excluding probes). **The board's (request-level) numbers were never affected** ⇒ for an exact cost reconciliation use the board, or switch the reconciliation page to the "business" caliber before comparing.
2. **Sessions spanning the window (E)**: 18 sessions whose first request predates the window are still active inside it and are currently excluded as a whole, missing 3,191 requests / $281.38 (3.2% of the board's cost).
3. **Calls without session affiliation (F)**: 381 direct API / scripted calls have no session; the board counts them, session statistics does not.
4. **Same-day lag of the reconciliation snapshot**: the reconciliation is a T+1 caliber, so **the current (today's) data is incomplete**; subtracting it directly from the board's real-time numbers will always produce a difference.
5. The board credits card does not change with the time range (see above).
6. **Probe scope in the degraded state**: when the board's pies and trends fall back to the pre-aggregated tables after the primary path fails, those tables are always "probes excluded" ⇒ in probe-included mode they will disagree with the hero summary (the payload carries a `degraded` flag in that case — it is not a silent degradation).
7. **The board and the provider reconciliation are no longer guaranteed to match digit for digit (2026-11-24, round 2)**: the board's source moved to the request-level logs while the reconciliation side reads `report_snapshots` / `usage_facts`. The two are still equal for periods where the fact table is intact; as soon as the window reaches back **before 2026-11-10 13:38**, the reconciliation side **undercounts** — the fact table lost ≈352,000 real business facts at that instant (2026-10-12 ~ 2026-11-10; evidence chain in the audit master file §C) while the request-level logs still hold that traffic. ⇒ **Explain a reconciliation shortfall against the board by "the two sources start their history at different points" — it is not a miscalculation**; for ledger-grade numbers use the reconciliation page (including its known same-day lag).
