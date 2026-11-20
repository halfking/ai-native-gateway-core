# Format Anomaly Page Data Guide

This page monitors two types of “something wrong”: an unexpected change in the upstream response **format**, or a request **failure**. The goal is to detect vendor-side changes before users do.

## Two Data Sources (Two Tabs)

- **Response Format Anomaly**: The upstream response structure does not match expectations (missing fields, changed types, Token extraction failure, etc.).
- **Request Error**: The request itself failed (timeout, rejection, invalid parameters, etc.).

The badge number at the top of the page equals the **sum** of the “Unresolved” counts from the two data sources, using the same criteria as the “Unresolved” filter on the list page.

## How “Unresolved / Resolved” Is Determined

- Newly detected anomalies are “Unresolved” by default; they are marked “Resolved” after manual confirmation and handling.
- “Resolved” does not mean the anomaly disappears automatically. It is a manually confirmed status used to distinguish between “reviewed” and “not reviewed.”

## How to Self-Check When the Numbers Do Not Match

1. The badge count and list count do not match: First check the filters (whether “Unresolved only” is selected and the time range).
2. A vendor update can cause a batch of similar anomalies to appear at the same time: Group by “Anomaly Type”; a batch of the same type usually has a single cause.
3. Both probe traffic and real traffic can trigger records: Before determining the impact, first identify which type of traffic the anomaly came from.
4. An anomaly that recovers automatically (when the vendor reverts the change) is not automatically marked “Resolved”: Manual confirmation is still required. This is intentional to prevent silent changes from being overlooked.
