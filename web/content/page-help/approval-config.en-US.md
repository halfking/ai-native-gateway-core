# Approval Configuration Page

Defines which actions require manual approval, including trigger conditions, approvers, and timeout policies.

## Definition

Policy = three elements: condition + approver + timeout; configuration changes only affect approvals triggered afterward.

## How to Self-Check When Numbers Do Not Match

- 1.Approval was configured but not generated: confirm the scope of the trigger conditions (tenant/key granularity) and the toggle status.
- Timeout-based automatic approval/rejection did not take effect: a timeout value of 0 means waiting indefinitely, so the request will not close automatically.
- Approvers are not receiving notifications: verify that the approver accounts are still active.
