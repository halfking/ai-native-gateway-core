# Queue card mini-window clears on empty batch

## Summary

Sliding-window batch `include_entries` used `json:"entries,omitempty"`. Go omits both nil and empty slices, so a later empty window left stale mini-window cells on queue cards.

## Change

- Encode included empty windows as `"entries":[]` via a pointer slice helper.
- Front-end `mergeCardWindowEntries` treats omitted entries as an empty list.

## Verification

- `go test ./admin/ -run 'SlidingWindow' -count=1`
- `cd web && pnpm exec vitest run src/utils/queueNodeCards.test.ts src/components/QueuePerspectivePanel.test.ts`
