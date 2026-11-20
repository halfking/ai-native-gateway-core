# Marketing Management Page Data Notes

This page manages four types of marketing tools: packages, top-up packs, promo codes, and campaigns.

## Four Tabs

- **Package**: The definition of a monthly package (how much subscription allowance is included and the price).
- **Top-up Pack**: An additional allowance pack purchased as a one-time purchase.
- **Promo Code**: A code that gives customers a discount or points after redemption.
- **Campaign**: A limited-time discount or bonus strategy (Draft → Active → Ended).

## Activation Rules

- **Discount Campaign**: After a tenant claims it, their next order is automatically reduced proportionally; it applies until used up.
- **Bonus Campaign**: Points are issued immediately upon claiming and are credited right away.
- A campaign in “Draft” does not affect any orders; once it is “Active,” it applies to tenants who have claimed it; after it is “Ended,” it can no longer be claimed, but claimed benefits can continue to be used according to their respective rules.

## How to Self-Check When the Numbers Do Not Match

1. A promo code shows “claimed N times,” but the order was not discounted: the discount applies only to the **next** order after claiming; earlier orders are not affected.
2. Bonus points were not credited: Confirm that the campaign status is “Active” rather than “Draft,” and that the tenant is on the claimant list.
3. The package price does not match the pricing page: a package uses a “bundle price,” which is separate from the Token-based list price; package users have their allowance deducted according to the package rather than being charged per Token.
