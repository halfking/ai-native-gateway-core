# Turn List Page Data Overview

This page organizes model calls by “conversation → turn”: a conversation is a continuous dialogue, and a turn is one question-and-answer exchange.

## Hierarchical Relationships

- Project / User / Client / Key are four grouping perspectives, allowing the same set of conversations to be viewed from different angles.
- Expand a conversation to see each of its turns: question summary, answer summary, elapsed time, and status.

## How “Turns” Are Counted

- One user question and one model response count as one turn.
- Turns within the same conversation are sorted by time.
- A turn may include multiple internal model calls, such as retries and tool calls. These are included in that turn and are not counted separately.

## Summaries and Status

- The system generates conversation summaries automatically. “Summarize now” manually triggers regeneration, and an in-progress status is displayed while the summary is being generated.
- When recording is unavailable, new conversations may temporarily have no turn details. This is a temporary collection-stage status, not data loss.

## How to Reconcile When Numbers Do Not Match

1. This page aggregates by “conversation,” while the Request Records page breaks entries down by “individual call”: a single request may be only part of a turn in the conversation view, so the counts on the two pages will naturally differ.
2. The more filter dimensions selected (project/user/client/key), the fewer conversations you will see: clear the filters and then compare the totals again.
3. There is a limit on how many turns can be loaded (one expansion retrieves the most recent 100 turns): for very long conversations, narrow the time range or use pagination to view more.
