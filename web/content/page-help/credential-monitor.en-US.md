# Credential Monitoring Page Data Guide

This page monitors the availability of upstream "credentials" (account keys used to connect to model providers), helping you identify issues before users report them.

## Tab Guide

- **List**: An overall health view with one row per credential (current status, latest error, reason for disabling).
- **Heatmap**: Shows when each credential encountered errors over time; darker colors indicate more errors.
- **Routing Log**: An event history at the credential level (enabling/disabling/switching).
- **Error Trend**: An hourly error curve that shows whether errors are sudden or ongoing.
- **Probe Health** (Super Admins only): The results of the system's active test calls to each credential.

## Status Label Definitions

- "Normal" = No recent failure records.
- "Manually Disabled" = An administrator manually took the credential offline; automated probes will not re-enable it.
- "Automatic Control" = The system automatically took the credential offline after consecutive failures (for example, after a fault was confirmed multiple times); you can manually force it offline or wait for automatic recovery.
- The disable-reason badge takes precedence over the health status: when a credential has both errors and a reason for disabling, the page displays the reason for disabling.

## Health Score and Error Count

- The error count includes **completed failed calls**, accumulated over the selected time window.
- A failed request counts only toward the credential used at that time; a successful retry does not remove the recorded failure.

## How to Self-Check When Numbers Do Not Match

1. The time windows do not match: Align this page's filters with the dashboard's time window before comparing.
2. "Probe" traffic consists of check calls initiated by the system and is counted separately from user traffic; determine credential availability based on both probe traffic and actual user traffic.
3. A recently recovered credential may continue to show an old error count for a short time: Charts are aggregated by hour, so complete results for the current hour will not be available until the next hour.
4. If the list is empty, check the filters first: A combination of credential type, tenant, and status filters may have removed all results.
