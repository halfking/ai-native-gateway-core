# Understanding Data on the Real-Time Topology Page

This page shows the gateway’s “Real-Time Traffic Map”: which nodes are online and where requests are currently flowing.

## Elements on the Map

- **Node** = a service point that can handle requests (an ingress node or provider node).
- **Link** = a connection between two nodes; the thickness/number of lines represents traffic volume.
- A request enters from the “user side,” is routed by the gateway, and exits through a “provider link” to retrieve an answer.

## Node Status

- Online/offline status comes from real-time node heartbeats. If Redis has an issue, the health status is downgraded to “Unknown”—this indicates a problem with the data collection channel, not that the node is actually unavailable.
- The **Direct Internal Connection** badge means the platform directly connects to and manages the provider node without using a third-party proxy.

## Real-Time Updates

- The page receives updates over a persistent connection and normally refreshes within seconds.
- During a brief disconnection, the page reconnects automatically. Requests made during the disconnection are not lost; the display simply catches up after a delay.
- “N requests passing through invalid nodes are hidden” = requests determined to have passed through invalid nodes are hidden by default and do not appear on the links; turn on the display toggle to show them when needed.

## Troubleshooting Number Mismatches

1. The real-time view is a snapshot of “right now” and is not directly comparable with cumulative totals for a “past period” on the statistics page. Use the request log page for reconciliation.
2. The numbers on a link show the number of requests that passed through it within a rolling window. As the window moves forward over time, the numbers naturally decline.
3. If a request appears stuck on a link, first check its status label: “In Progress,” “Completed,” and “Failed” indicate different stages.
4. If all nodes show “Unknown,” the message channel has most likely failed. Refresh the page or wait for it to reconnect to restore the status.
