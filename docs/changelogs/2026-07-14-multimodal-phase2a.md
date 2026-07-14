# 2026-07-14 - Multimodal Phase 2A

## Direct main delivery

- Delivery mode: direct `main` merge; no pull request is created.
- Commit marker: `direct-main`.
- Scope: local attachment reliability, strict handling, Anthropic extraction,
  failover manifest reuse, and executable S17-S28 audit scenarios.

## Commit chain

- `58f31d74d` `feat(attachments): hash-shard local files [direct-main]`
  - Phase 2A steps 1-2: SHA256 two-level path shards, cross-request dedup,
    legacy relative-path reads, and existing atomic local writes.
- `4b86e92ff` `feat(attachments): enforce manifest status and strict mode [direct-main]`
  - Phase 2A steps 3-4 and 6: persisted manifest states, strict-by-default
    failure handling, and Anthropic attachment extraction.
- `1a1cff8cd` `feat(attachments): bound uploads and reuse failover manifest [direct-main]`
  - Phase 2A steps 5 and 7: body-read deadline/cancellation, immutable retry
    input, and executable S17/S18/S20/S21 audit evidence.

## Verification

- `go test ./...`
- `go vet ./...`
- `python3 docs/全方面测试/tools/attachment_audit.py --json`

## References

- `docs/会话优化v2/05-融合实施方案-附件与模型契约.md`
- `docs/全方面测试/09-多模态与模型契约场景.md`
