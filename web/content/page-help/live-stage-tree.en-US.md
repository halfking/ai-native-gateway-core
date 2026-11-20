# Request Stage Tree Page Data Guide

This page breaks the lifecycle of a request into ten stages arranged along a timeline, making it easy to see where a request got stuck when an issue occurs.

## Ten Stages (in Occurrence Order)

Authentication / Rate Limiting → Masking → Approval → Injection Detection → Routing → Compression → Provider / Token Pool → Time to First Byte → Masked Data Write-Back → Metering / Response.

- The outbound track (left → right) shows how the request is sent; the inbound track (right → left) shows how the response comes back.
- A request does not necessarily go through every stage: if approval is not triggered, the “Approval” stage shows “Not Applicable.”

## Seven Stage Statuses

Not Reached / In Progress / Completed / Not Applicable / Failed / Warning / Not Measured.

- “Failed” means the request was blocked at this step (for example, injection detection matched or approval was denied).
- “Warning” means the request was allowed through, but suspicious activity was recorded.
- “Not Measured” means there is no timing data for this stage; it does not indicate failure.

## Stage Duration

- The length of each stage’s bar represents how long it took; the time from “Time to First Byte” to “Response” is the model’s answer-generation time and is usually the longest.
- Total duration ≠ the simple sum of all stages: some stages occur in parallel.

## How to Self-Check When the Numbers Do Not Match

1. For requests marked “In Progress,” stage durations are still increasing; the figures become final only after completion.
2. If you cannot find a request, check the filters first: the page only displays requests that match the current filters and fall within the selected time window.
3. Many “Not Measured” stages usually mean the request used the legacy pipeline format (earlier data). This is a historical data format, not a system failure.
4. For failed requests, use the “Failed Stage” as the source of truth: when investigating an issue, start with the first stage marked “Failed,” as later stages usually do not run.
