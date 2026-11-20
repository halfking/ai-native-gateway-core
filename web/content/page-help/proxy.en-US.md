# Proxy Management Page Data Guide

This page manages the "proxy egress" used by the gateway when accessing upstream services: subscription sources, node pools, and the egress currently in use.

## Three Tabs

- **Status Overview**: Overview cards (subscription count, total node count, dialable/healthy count) + currently selected egress + automatic traffic-switching status.
- **Subscription Management**: Proxy subscription sources (links used to import a batch of nodes), with an option to filter egresses using a "region blacklist".
- **Node Management**: Manual registration and maintenance of individual nodes.

## Key Definitions

- **Dialable** = the node can currently establish a connection; **Healthy** = the node has passed consecutive probes. A node may be dialable but still fail repeatedly, in which case it is not considered healthy.
- **Automatic Traffic Switching**: When the current egress reaches the failure threshold, the system automatically switches to the next node. The "consecutive failure count" increases in real time and resets after a success.
- "Most Recent Active Probe" refers to when the system initiated the connectivity check, not when the business request occurred.

## How to Self-Check When Numbers Do Not Match

1. Total node count ≠ dialable count: This is expected—the total count reflects registered nodes, while the dialable count reflects the number currently reachable.
2. Business requests still fail after traffic switching: Traffic switching runs on a probe interval and completes within a few seconds. If failures persist, check whether the "consecutive failure count" is still increasing.
3. Node count does not change after a subscription update: The subscription source may have the same content; compare node fingerprints before and after the update rather than comparing counts.
4. Blacklisted regions are not filtered: Region codes must exactly match the region values reported by nodes (case-sensitive). An incorrectly configured value does not produce an error, but it will not filter anything.
