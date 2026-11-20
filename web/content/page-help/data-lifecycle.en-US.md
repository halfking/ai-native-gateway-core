# Data Lifecycle Page Guide

This page manages the “storage, movement, and cleanup” of data: which data in the database takes up space, when it is archived, and how it is reclaimed.

## Tab Overview

- **Storage Overview**: how much space each table uses and how quickly usage is growing.
- **Hot Table Migration & Partition Cleanup**: store large, frequently written tables in monthly partitions, and archive older months in their entirety.
- **Aggregate Recovery & Cleanup**: rebuild from detailed records when aggregate data is corrupted.
- **Storage Configuration**: database connection and storage location settings.
- **Log Management**: runtime log retention and cleanup.
- **Session Attachments / Large Fields / File System**: space usage and maintenance for file-based data.

## Key Metrics

- **Retention period** = how long data remains in the database. Expired data becomes “eligible for cleanup,” and the number of affected rows can be previewed before cleanup.
- “Eligible for cleanup” does not mean “deleted”: cleanup requires a preview and manual execution. If new data is written between the preview and actual execution, the actual row count may differ slightly.
- Table space usage rises before it falls: while space reclamation operations are running, disk usage initially doubles (old and new files coexist), and space is released only after the operation finishes.

## Safety Mechanisms

- Every cleanup action requires a “preview” before “execution,” as two separate steps.
- Archiving moves data to slower storage; it does not delete it. Archived data can still be retrieved for restoration.

## How to Self-Check When Numbers Do Not Match

1. Usage figures come from database statistics and consistently differ from file sizes shown by the operating system (databases have internal reserved space). Having the same order of magnitude is sufficient.
2. An increase in “eligible for cleanup” rows over time is normal (data aging), not abnormal growth.
3. Row count fluctuations during rebuilding or migration are temporary. The final state after the task finishes is authoritative.
4. First, check the tab: the same table uses different metrics in different tabs (the overview shows space usage, while the partition page shows row count distribution).
