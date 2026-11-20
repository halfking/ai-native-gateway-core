# Self-Update Page Data Notes

This page manages version upgrades for the gateway itself: check for the latest version → send an alert → install after administrator confirmation.

## Three Statuses

- **Version source not configured**: This node has no setting for “where to check for new versions,” so automatic checks are not enabled.
- **Version status unknown**: A check was performed, but no result was obtained (network issue or source unreachable).
- **Normal**: The current and latest versions are known and can be compared.

## Upgrade Process

1. “Check for Updates Now” queries the configured version source for the latest version number.
2. When a new version is available, an alert is generated; **it will not install automatically**.
3. After the administrator confirms the installation, the upgrade process begins. When it finishes, the service automatically restarts with the new version.

## Definitions

- “Current version” is the version **this node is running**; in multi-node deployments, nodes may run different versions, and this page shows only the current node.
- The service briefly restarts during an upgrade: requests are handled by other nodes during this period (in multi-node deployments) or are briefly interrupted (in single-node deployments).

## How to Check for Version Mismatches

1. The old version is still shown immediately after an upgrade: refresh the page once to update the cached version information.
2. “Latest version” does not match the official release page: this page reads from the **configured version source**. If it is not synced with the official source, the configured source takes precedence. Check the version source URL first.
3. Update checks remain “unknown”: the version source URL is unreachable. Test connectivity to that URL directly from the deployment host.
