# Audit Log Page Data Notes

This page records “who changed what and when”—a chronological log of administrative operations for retrospective tracing.

## Each Record Contains

Operator, time, action type (e.g., changing configuration, disabling a key, adjusting parameters), operation target, and values before and after the change.

## Record Scope

- Only **control-plane operations** (write operations performed through the admin console and operations API) are recorded; regular users’ model calls are not included here. See the Request Logs page for those.
- Read operations (view only, no changes) are not recorded by default.

## Query Tips

- Combining “operator + time range” is the most effective filter: first identify the person, then narrow down the time.
- “Values before and after the change” are the authoritative source for verifying “when and by whom the configuration was changed.”

## What to Check When the Numbers Do Not Match

1. You changed the configuration, but cannot find a log: confirm that the operation went through the admin interface/admin API (the channel instrumented for auditing). Directly modifying the database bypasses the gateway and leaves no trace.
2. The time is off by several hours: log times use Coordinated Universal Time (UTC), so you must convert local time yourself (Beijing time is UTC+8).
3. There are two records for the same action: each sub-operation is recorded separately (e.g., “Update configuration” may be split into “read old value” and “write new value”). Use the most complete record as the authoritative one.
