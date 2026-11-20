# Route Rewrite Page

Manually override routing: direct traffic for a specific model or key to a designated target.

## Overview

Rewrite rules take priority over automatic route selection. Changes take effect immediately and can be revoked at any time.

## How to Troubleshoot When the Numbers Do Not Match

- 1.The rewrite did not take effect: Check the scope of the rewrite conditions (model/key/tenant dimension). Conditions that are too narrow will not match the traffic.
- Traffic still goes to the old target after revocation: The routing decision was cached for a few seconds and will resume shortly.
