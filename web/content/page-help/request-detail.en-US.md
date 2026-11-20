# Request Details Page Data Guide

This page shows the **complete record of a single request**: the entire process from entering the gateway through returning the answer, along with every associated figure.

## Three Sections

- **Basic Information**: timestamp, API key, model, status, and total duration.
- **Process Details**: duration and result for each stage (authentication, route selection, upstream call, data masking and restoration, and metering).
- **Content and Usage**: summaries of the content actually sent and received, input/output text volume, and the fee for the request.

## Key Definitions

- **Total duration** = the time from receiving the request to returning the complete answer. It includes both gateway processing time and upstream generation time, which are listed separately in the details.
- **Fee** is calculated using the price table in effect when the request occurred; later price changes do not alter the fee for that historical request.
- Content summaries may be truncated for display (to protect performance). To view the complete content, select “View Full Text.”

## How to Self-Check When the Numbers Do Not Match

1. The fee for this request does not match the model catalog price: first confirm the request time and the price-change time—the price is frozen at the **request timestamp**.
2. The input text volume is higher than your own count: system counting includes message wrappers (system prompts and conversation history), not just the text you entered.
3. The status is “Success,” but the user says they did not receive the response: success from the gateway’s perspective means that the “upstream returned the complete response.” A client network failure afterward is an issue in the latter part of the chain, so check the request’s action timeline to confirm the response-delivery stage.
4. Retry traces: a single request may internally try several candidates, but the final status records only the last attempt. The process details show the complete chain of attempts.
