# Data Notes for the System Settings Page

This page is the platform-wide “master control panel”: global settings such as interface mode, compression behavior, and security toggles are all configured here.

## How Settings Take Effect

- Most settings **take effect as soon as they are saved** (the next request uses the new values).
- Items marked “Requires restart” take effect after the service restarts; the page displays a separate notice for these items.

## Simple/Expert Modes

- “Simple”: The home page shows the integration list, with all features grouped under “Advanced”—for people who do not use the platform often.
- “Expert”: Shows the full navigation and all pages.
- This only switches the **interface view**: both modes use the same data and the same statistical definitions, so the figures do not vary by mode.

## Compression Mode (Handling Very Long Conversations)

- Mode 1 (predictive): Compresses proactively when approaching the model’s capacity limit.
- Mode 2 (responsive, recommended): Compresses after receiving a “too long” error and automatically retries—compressing only when necessary to reduce overhead.
- Compression only affects **how content is organized before being sent to the model**; it does not delete the original conversation. Statistics and billing are based on the calls actually made.

## How to Check When Figures Do Not Match

1. If a setting was changed but the behavior did not change: First confirm whether the item is marked “Requires restart,” then check whether a success message appeared after saving.
2. If the figures differ between the two modes: This should be impossible. If it happens, the time windows or filters are different; align them first.
3. If a switch is on but some requests do not use it: Some switches apply at the tenant/key level, while global switches serve only as a fallback. For a specific request, the most granular configuration takes precedence.
