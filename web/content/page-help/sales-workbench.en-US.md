# Quotation Workbench Page Data Guide

This page is an internal workbench for preparing quotes for customers: first register “what we can sell,” then register “what the customer wants,” and the system will combine them into a quote.

## Four Tabs

- **Supply Pool**: A list of models currently available for sale on the platform (including cost price, currency, and confidence, all of which are derived by the server and cannot be manually edited).
- **Demand Pool**: The models and usage volumes the customer wants.
- **Quote**: A formal quote created after supply and demand are matched (with a version number and support for approval and sharing).
- **Dashboard** (super administrators only): An overview of the quotation funnel and cost drift.

## Key Definitions

- **Cost Baseline**: The cost of a quote line comes from the cost price table on the Pricing Management page. The cost on a quote is a snapshot taken when the quote is generated; subsequent cost changes do not retroactively affect issued quotes.
- **Version**: Each revision to the same quote generates a new version. Historical versions are read-only and retained for traceability.
- **Cost Drift Badge**: An alert appears when the cost price used for a quote differs from the current cost price by more than the threshold, helping prevent quotes from being issued based on outdated costs.

## How to Self-Check When the Numbers Do Not Match

1. A model cannot be found in the Supply Pool: First confirm that its cost price has been registered in Pricing Management. Models without a cost price cannot be sold.
2. The cost on a quote differs from the current cost price: The quote has frozen the cost at the time it was generated. Check the “Cost Drift” badge to determine whether a revised quote is needed.
3. An item has disappeared from the Demand Pool: Demand converted into a quote is archived from the Demand Pool and can be found on the Quote page.
