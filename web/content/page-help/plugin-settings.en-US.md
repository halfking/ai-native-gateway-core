# Plugin Configuration Page Data Guide

This page lets you choose which implementation each platform “plugin slot” uses (for example, whether login authentication uses a built-in component or an external identity service).

## How to Use

- Each plugin slot has its own card: the active implementation + a dropdown of available options.
- **The service must be restarted after saving for changes to take effect**: The page saves the configuration, and the switch occurs at the next startup—successfully saving does not mean the switch has been applied.

## Configuration Source

- “Configuration Source” appears at the top of the page, indicating whether the current value comes from the database or an environment variable.
- Even if an environment variable–backed setting is changed on the page, the environment variable takes precedence—to make the switch permanent, update the deployment environment.

## How to Self-Check When Data Does Not Match

1. Login behavior did not change after switching: First, confirm that the service was restarted with the new configuration.
2. The status shown on the page does not match actual behavior: Use “Configuration Source” as the source of truth—environment variables take precedence over values saved on the page.
3. A plugin slot has no available options: The candidate implementation for that slot has not been deployed; install the corresponding component first.
