# Standard Model Pricing Page Data Notes

This page defines the “selling price”—how much a user pays each time they use a standard model. It complements the “cost price” page: the cost price is what you buy in; this page is what you sell at.

## Pricing Rules (Three)

- **One row per standard model**: Users only care about “how much it costs to use this model,” not which provider powers it.
- **Global benchmark × discount factor**: Dimensions without separate pricing (input/output/cache) are calculated automatically by multiplying the global benchmark price by the discount.
- **Manual pricing takes precedence**: Fields marked “Manual” are frozen and are not affected by global price adjustments—this is used to protect pricing for special models.

## Multimodal Billing

Text usage for image/audio/video models is billed separately at their **dedicated rates** and is not combined with standard text usage; model rows that include multimodal pricing are labeled “Includes multimodal pricing.”

## Consistency with Other Pages

- Changes on this page take **effect immediately** for subsequent usage billing; same-day trends and invoices are reflected after the next snapshot.
- The “Plans & Top-ups” prices shown to users come from this page. If the two differ, this page is authoritative, and the sync task should be checked.

## How to Troubleshoot Number Mismatches

1. User-side prices did not change after a price adjustment: Check whether the user-side cache was not refreshed, or whether the model’s field is marked “Manual.”
2. Two selling prices for the same model: One may use the global discount while the other uses manual pricing—check the “Manual” label.
3. Invoice amount does not match usage records: Confirm the order of the pricing timestamp and the price-adjustment timestamp; invoices spanning a price adjustment are calculated in segments.
