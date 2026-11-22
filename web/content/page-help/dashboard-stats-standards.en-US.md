# Calculation Standards for the Board and Session Statistics

The "Board" and "Session & Statistics" tabs summarize the same stream of model calls under **two different calculation rules**. This page spells out how every number on each tab is produced, which source it comes from, which time window it covers, and gives a side-by-side table of why the two tabs disagree — for aligning data and tracing deviations.

> Measured on production, 2026-11-23 02:27 (Asia/Shanghai), cross-checked against read-only production database queries.

## How the two tabs relate

| | Board | Session & Statistics |
| --- | --- | --- |
| Statistical entity | One model call counts as one row (**request level**) | One session counts as one row, carrying that session's **cumulative values** (**session level**) |
| Source of truth | Request-level fact table `usage_facts` (Redis cached baseline + live delta) | Session summary table `session_summaries` (live query) + materialized views (cached surface) |
| Time window | Calendar days in the system time zone: midnight of today-(N-1) → now | Panel = rolling N days (first request inside the last N days); aligned row = whole Shanghai calendar days |
| Probe traffic | Excluded by the request-level fact table's traffic class | Excluded by the session-side "session universe" rules |

**Bottom line first**: the two tabs' numbers **are not supposed to be equal** — different entities (request vs session), different windows, different exclusion rules. Any cross-tab comparison must align those three first; you cannot simply subtract one from the other.

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
| Pie charts (model / provider / tenant / client…) | Requests / tokens / cost grouped by dimension | From the per-minute statistics table — not the same source as the cards above |
| Usage trend chart | Requests / tokens / cost / credits bucketed over time | Also from the per-minute statistics table |

### How the time window is computed

- "Today" = midnight today (system time zone, default Asia/Shanghai) → now; "Last 7 days" = midnight 6 days ago → now; "Last 30 days" likewise. **The start is a calendar-day midnight, the end is the current moment** — not a whole day.
- A custom range aggregates the two selected days, including the entire last day.
- The page refreshes every 10 seconds; when the live channel is up, newly completed requests are added immediately.

### The credits card (a special rule — please note)

The credits card does **not** show credits consumed inside the selected window: the credits column of the request-level fact table is currently always 0 (measured: all 298,037 rows are 0), so what the card shows is the **live delta accumulated after the cached baseline**. Therefore:

- Switching between "Today / Last 7 days / Last 30 days" leaves the credits card **exactly unchanged** (measured: all three show 86,925).
- The usage trend chart on the same page carries a separate credits total (per-minute statistics table, 1,767,183 over 7 days) that is not the same number as the card.

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
| Top 5 users / Top 5 providers | Session-analytics overview endpoint | The provider is derived from the model-name prefix and ranked by request count; **no cost** is included |
| Turns cross-check | Session request count vs turns count | When the gap exceeds 10% the page shows a notice |

### The session universe (which sessions are counted)

A session is counted only when all three hold:

1. It is registered in the session table;
2. It has an owner (taken from the user key or the API-key owner key; if both are empty it is not counted);
3. It is not a probe session: neither manually flagged as a probe, nor matched as a self-check / deployment-gate / test / verification session by the "owner / client type / title" pattern.

### Why the cost here is lower than on the board

Rule 3 of the session universe drops **real business sessions** that are classified as probes. Measured on the production database (same 7-day window):

- The 15,898 sessions excluded by the session side have their primary request classified as **100% business traffic** in the request-level fact table, worth $6,452.73 in total;
- Over the same window, traffic actually classified as probe in the request-level fact table totals only **$1.81**.

In other words: although both tabs claim to exclude probes, they are **not excluding the same requests**. This is the primary reason the Session & Statistics cost sits well below the board's.

## Side-by-side numbers (measured 2026-11-23, 7-day window)

| Metric | Board | Session & Statistics (panel) | Session & Statistics (aligned row) |
| --- | --- | --- | --- |
| Statistical entity | requests | sessions + session-cumulative requests | sessions / requests |
| Requests | 65,774 | 72,098 | — |
| Cost (USD) | 8,013.93 | 2,717.62 | 2,468.73 (internal cost) |
| Credits | 86,925 (window-independent) | — | 0 |
| Tokens | 3,245,804,009 | — | — |
| Success rate | 95.31% | — | 94.64% |
| Sessions | — | 19,235 | 17,390 |

Matching each gap to its cause:

- **Requests 72,098 > 65,774**: the session-side request count is the cumulative request count of the selected sessions, so a session spanning the window boundary contributes its post-window requests in full; on top of that, the session table contains duplicate rows (measured: up to 30 duplicate rows for the same session key), which are aggregated in as well.
- **Cost 2,717.62 < 8,013.93**: the session universe drops the $6,452.73 of business traffic described above as probes; in addition the board also counts calls that have **no session affiliation** (direct API calls, scripted calls), which account for roughly a fifth of the board's cost.
- **Credits 0 vs 86,925**: neither the session side nor the request-level fact table currently holds a trustworthy credit stock; the number on the page comes from the live delta.
- **Success rate 95.31% vs 94.64%**: request-level against request-level, but one is computed from `usage_facts` and the other from the request-log materialized view, and the windows differ.

## What to check when the numbers do not match

1. **Align the window first**: board = from a calendar-day midnight; Session & Statistics panel = rolling N days; aligned row = whole Shanghai calendar days. Even for the same preset the three are not the same window.
2. **Then align the entity**: compare requests with requests and cost with cost; never compare a session count with a request count.
3. **Look for degradation markers**: when a card shows "degraded / untrustworthy / not computed", those zeros do not mean "no traffic".
4. **Check the probe switch**: the board's "exclude probes / include probes" toggle only switches the hero summary; the distribution charts and the trend chart always use the business caliber.
5. **"Today" used to fail (fixed 2026-11-23)**: the "Today" preset of Session & Statistics reproducibly hit the 30-second timeout because the join index was missing on the database side (index added 2026-11-23: the query went from 121.8 s to 0.19 s). If the page shows that error again, the index has gone missing again — ask the on-call engineer to check that `idx_session_dim_session_key` is in place.

## Known limitations (measured 2026-11-23)

1. **Session-side cost and request count are inflated (double counting, not yet fixed)**: the cost and request count of Session & Statistics come from the session-summary aggregation, but `sessions` is a **day-partitioned session history** (the same session gets a new row every day) and the statistics join does not de-duplicate per session ⇒ the same session is counted several times. Measured on the production database on 2026-11-23 (same 7-day window): the session-side cost comes out about **61 %** and the request count about **36 %** above the values de-duplicated per session (the most expensive session was counted 3 times). **The Board numbers (request level) are not affected** ⇒ when comparing cost, use the Board, or de-duplicate the session-side numbers first.
2. The board credits card does not change with the time range (see above).
3. The board trend chart (per-minute statistics table) and the cards above it (request-level fact table) differ by about 13.8% over a 30-day window (160,451 vs 140,969 requests).
4. Probe exclusion does not remove the same traffic on both sides (see above).
