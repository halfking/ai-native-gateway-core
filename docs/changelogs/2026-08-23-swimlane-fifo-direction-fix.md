# 2026-08-23 — Swim lane FIFO direction fix (oldest left, newest right)

## TL;DR

- Backend lane builder now emits ASC + `lastTiles()` (newest window at tail).
- `SwimLaneTrack` uses `flex-start` so tiles grow left→right from the lane origin.
- Production repro on build #1692: `flex-end` clustered all bars on the right despite ASC timestamps.

## Verification

```bash
go test ./admin/ -run 'BuildLiveStreamLanes|LastTiles|SnapshotFromDimensionQueues_Requests|LaneCapKeeps' -count=1
cd web && pnpm exec vitest run src/components/SwimLaneTrack.test.ts src/composables/liveStreamStore.test.ts
```

Browser (pre-deploy repro on https://llm.kxpms.cn/dashboard → 按供应商):

- Before: first bar `left=1354`, track `justify-content: flex-end`
- After CSS inject `flex-start`: first bar `left=222` (track origin), timestamps ASC left→right

Deploy required for permanent fix on production.
