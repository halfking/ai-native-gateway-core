# Admin reader inventory verification plan

> **For agentic workers:** Execute inline with red/green checkpoints and independent Standards/Spec review.

**Goal:** Resolve and explain the five reported admin inventory failures without weakening the gates.

**Architecture:** Preserve the upstream reconciliation f13c2289d. Prove each inventory change against source, then strengthen the control-plane evidence predicate so Go comments cannot impersonate code. Production query behavior is outside this patch.

**Tech Stack:** Go AST, existing admin census gates, Go overlay mutations.

### 1. Establish the baseline

- [x] Pin clean origin/main a70cb460c in an isolated worktree.
- [x] Re-run the five named tests; all pass after upstream f13c2289d.
- [x] Compare original failure output to providers removal, tenant-source extraction, lifecycle counts, monitor source, usage membership and indirect-reader classification.
- [x] Complete `go test ./admin -count=1` against the baseline (171.6s).

### 2. Prevent stale evidence hidden in comments

- [x] Add `admin/inventory_evidence_test.go`, exposing `inventoryEvidenceInCode(source, evidence string) bool` to the existing evidence gate.
- [x] Observe raw substring matching fail line-comment, block-comment, malformed-source and empty-evidence fixtures.
- [x] Parse Go with comments, reject empty/malformed input, and accept only exact matches outside comment ranges. Search later occurrences so a stale comment cannot hide real code.
- [x] Run fixture and real inventory gates; ordinary and integration configurations compile.
- [x] Reject CR-normalized AST end offsets using raw source comment boundaries; red/green CR/CRLF fixtures prove the fix.
- [x] Reconcile waterfall in both control-plane and stop-write tables against the canonical query and migration 824 projections; pin its S4 assessment in a regression test. Retain errors_out for v1-only probe/internal request populations; canonical projection alone does not prove mirror coverage.

### 3. Prove the gates still reject regressions

- [x] Use isolated Go overlays to corrupt evidence, call-site counts, reader membership, unassessed membership, tier evidence and blind-spot membership individually. Require the designated test and intended assertion to fail; compiler errors do not count.
- [x] Mutate the evidence predicate and its caller independently; confirm unit fixtures and the real gate catch both.
- [x] Run complete admin tests, related SQL indirection tests, `go build ./...` and `go vet ./admin`.
- [x] Review exact commit SHA on both axes, save Task Stop Summary, merge without squashing concurrent changes, and push main (0ebe9bf8e → ff7b8e382).
- [x] Confirm the merged main passes the complete admin package, build and vet.

Final cleanup follows the audit report/lessons documentation checkpoint: push that checkpoint, then remove only this task's worktree and branch.
