# Key List Page Data Notes

This page manages the gateway’s “keys”: each key represents an identity that can initiate model calls.

## Fields in Each Row

- Name and prefix: A human-readable identifier for the key; the full key is displayed only once, when it is issued.
- Status: See the four statuses below.
- Usage and quota: The percentage of the quota that has been consumed.

## How the Four Statuses Are Determined

| Status | Condition |
| --- | --- |
| Pending activation | The key has been issued but has not completed its first validation. |
| Enabled | The status is normal + the key has not expired + the key has not been disabled; all three conditions must be met. |
| Expired | An expiration time has been set and has passed. |
| Revoked | The key has been manually disabled. |

Note: “Enabled” requires all three conditions to be met. If a key is expired or disabled, the page will show “Expired” or “Revoked” even if the backend status field still says “Normal.”

## Usage Statistics

- The usage figure counts **completed calls**; in-progress requests are not included.
- Usage data may be slightly delayed: a call that was just initiated may take a short time to appear in the usage figures.

## How to Self-Check When Figures Do Not Match

1. Check which status filter tab is selected: the page has status-based tabs at the top, and the list contents change when you switch tabs.
2. Expiration is determined by server time; local clock differences do not affect the determination.
3. System built-in keys (used by the platform itself) always appear in the list and are excluded from standard statistics.
4. Quota and usage come from two data sources and update at different intervals: usage is updated in near real time, while quota changes take effect immediately. If the figures do not match, refresh the page first.
