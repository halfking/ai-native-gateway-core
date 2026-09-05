# C2-C4: Deep Compression

> Status: `RECONSTRUCTED-DRAFT`
> Engine mapping and rollout are `NEW-DESIGN`; referenced OmniRoute rules are `SOURCE-VERIFIED` where paths exist.

## Scope

The proposed deep pipeline covers Caveman-style regex rules, protected ranges, RTK tool-output filtering, strategy selection, Memora/session facts, mechanical trim, and optional LLM summarization. These are separate responsibilities and must not be collapsed into one undocumented `Apply` function.

## Translation constraints

- Go regexp uses RE2 and does not support JavaScript lookbehind; every such rule needs an equivalent rewrite and a golden test.
- Extract and restore code blocks, URLs, paths, tool calls, and structured content before applying text rules.
- Keep stage ownership explicit: client-side context trim, body-level compression, RecoveryCoordinator, Memora, and LLM summary must each have one owner.
- Fail open on parse, budget, or upstream summarizer errors.

## Gates and non-goals

`NEW-DESIGN`: require a corpus of protected-content fixtures, token/latency budgets, quality scoring, race tests, and an operator-visible compression event. Production implementation is deferred; this document does not authorize schema changes or a new summarizer service.
