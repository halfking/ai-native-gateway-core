# Operations Overview Page Data Guide

This page serves as a “global command center” for multiple deployment locations, providing an at-a-glance view of the central service, each region, deployment nodes, and data plane health.

## Four Sections

- **Central Instance**: Operating status and version of the central service.
- **Regional Distribution**: Distribution and status of deployment locations by geography.
- **Deployment Nodes**: Recent deployment records (time, version, and result).
- **Data Plane**: Size and health of core data tables.

## Status Definitions

- Node status is based on periodic reports from each node. “Offline” means no message has been received within the reporting interval; the node may have stopped, or the network connection may have been interrupted.
- Version numbers are based on node-reported data. Multiple versions within the same region indicate a transitional state during a rolling upgrade.

## Data Freshness

- Data on this page is polled and refreshed at fixed intervals (not updated in real time); a newly completed deployment will appear after the next refresh.
- In “Latest N Records,” N is the maximum number of records displayed on the page, not the full history. For complete history, go to Deployment Management.

## Self-Checks When Numbers Do Not Match

1. Confirm the timing first: there is a polling interval between the data on this page and the status shown locally on a node.
2. If the regional total does not equal the sum of individual nodes: nodes that have not reported are treated as “Unknown” and excluded from the regional total.
3. Version differences do not necessarily indicate an issue: having old and new versions coexist during a rolling upgrade is expected, and the versions should converge once the upgrade is complete.
