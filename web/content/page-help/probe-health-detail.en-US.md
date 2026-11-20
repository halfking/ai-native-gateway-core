# Probe Health Details Page

A detailed profile of each probe target: historical probe success rate, latency distribution, and failure details.

## Metric Definitions

Probe = a test call initiated proactively by the system; the success rate is calculated based on the number of probes and is separate from business traffic.

## How to Self-Check When Numbers Do Not Match

- 1.All probes passed, but business requests failed: The probe path may differ from the actual path (such as using a simplified request); use actual traffic as the source of truth.
- Latency differs from request records: Probes use fixed, small requests, so their latency is naturally lower.
