# Popular Models Follow-up Logic Points

| ID | Scope | Estimated LOC | Depends on |
|---|---|---:|---|
| LP1 | Tenant-scoped recent ZSET and SQL usage query | 130 | - |
| LP2 | Per-tenant response cache and picker integration | 80 | LP1 |
| LP3 | Configurable lookup window and SQL short-circuit | 80 | LP1 |
| LP4 | SSE overlay outcome metric | 100 | - |
| LP5 | Focused unit coverage | 180 | LP1-LP4 |

All production logic points remain below 300 LOC. Tests are isolated by public seams: picker aggregation, telemetry model recording, configuration parsing, and overlay outcome recording.
