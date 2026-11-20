# Approval List Page Data Notes

This page is the central place to handle matters requiring a manual decision: certain sensitive operations or over-budget requests are put on hold first and await administrator approval here.

## Status Transitions

Pending Approval → (Approved | Rejected) → Archived.

- Items in "Pending Approval" remain on hold until they are processed (for example, a request stays in a waiting state while awaiting approval).
- Approvals follow a timeout policy: items not processed by the deadline are automatically approved or rejected based on the configuration. The details page states which policy applies to each item.

## Relationship Between the Configuration Page and the List Page

- "Approval Configuration" defines **which situations** require approval, who approves them, and the timeout period.
- The "Approval List" contains tasks **actually generated** according to the configuration.
- Configuration changes affect only approvals generated afterward and do not affect existing approvals in the list.

## How to Self-Check When Counts Do Not Match

1. The number of list entries does not match the number of notifications: Notifications may include items already handled by someone else. Use the "Pending Approval" tab on the list as the source of truth.
2. Two approval records appear for the same item: Each was triggered by a different policy, and the details page identifies the trigger source.
3. An approval was granted, but the function did not take effect: There is a brief delay before changes take effect. If it still has not taken effect after more than one minute, check the corresponding feature page.
