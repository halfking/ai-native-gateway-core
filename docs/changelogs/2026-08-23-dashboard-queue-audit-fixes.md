# 2026-08-23 — Dashboard queue audit fixes

## TL;DR

Post-audit fixes for live-stream selective trim and queue perspective UI:
rank label uses visual position; batch status load logs pipeline failures; main-queue trim test; removed no-op `trimLiveStreamQueue` calls.

## Verification

```bash
go test ./admin/ -run 'SelectiveTrim|Priority|Trim' -count=1
pnpm exec vitest run web/src/utils/queueNodeCards.test.ts web/src/components/QueuePerspectivePanel.test.ts
```
