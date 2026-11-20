# Simple Mode Overview Page Data Guide

This page is the at-a-glance version of the dashboard: overall status + usage + quick actions, designed for users who do not need to monitor it continuously.

## The Page Has Three Sections

- **Summary Cards**: Key figures such as today’s request volume, success rate, and costs.
- **System Status**: Whether key components are operating normally, clearly indicated with color and text.
- **Last 7 Days Trend**: A daily bar chart showing changes in request volume.

## Metric Definitions

- Summary cards use a **today-based metric** (from midnight today to the present, with days rolling over at midnight UTC).
- The trend chart and the Expert Mode dashboard use the **same data source**: figures shown in either mode will be consistent.
- The trend chart plots only “days with data”: on Day 1 of a new deployment, having only one bar is expected, and no artificial zero-value bars are added.

## How to Self-Check When Figures Do Not Match

1. “Today’s Requests” rolls over at midnight UTC: Before 8:00 a.m. China Standard Time, the chart’s “today” still shows the last bar from yesterday.
2. The denominator for the success rate is all requests, including blocked requests: If the figure differs from “successful requests ÷ the total you expected,” first check whether any requests were blocked.
3. The cost metric uses only one basis: Internal costs (USD) and external credits are not combined for display and should be checked separately.
4. A system status item shows “Normal,” but a page will not open: The status check confirms that the component is running, not that every individual feature is available; for specific issues, see the instructions on the corresponding page.
