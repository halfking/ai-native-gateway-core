# Routing Resolve Candidate Ordering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make routing-v2 resolve always show every matched candidate, including unavailable nodes, preserve backend priority ordering, and allow super_admin users to drag candidates to persist a new priority order.

**Architecture:** Keep availability as a read-only fact from `v_routable_credential_models.is_routable` and `unavailable_reason`. The resolve list will render the full response without a client-side filter. Dragging a full candidate list assigns contiguous `manual_priority` values and submits them through one super_admin-only transactional endpoint, so the ordering is durable and cannot partially update.

**Tech Stack:** Go HTTP handlers, pgx database transactions, Vue 3 `<script setup>`, TypeScript, native HTML5 drag events, existing request/auth and design-token helpers.

---

### Task 1: Add transactional batch priority endpoint

**Files:**
- Modify: `admin/routing.go` near `handleRoutingCandidateBindingUpdate`
- Modify: `admin/handler.go` route registration
- Create/modify: `admin/routing_candidate_binding_test.go`

- [ ] Add `handleRoutingCandidateBindingReorder` for `PATCH /api/routing/candidate-bindings/reorder` with body `{ raw_model: string, items: [{ credential_id: number, manual_priority: number }] }`.
- [ ] Validate non-empty model, non-empty items, positive credential IDs, contiguous unique priorities starting at 1, and a maximum list size consistent with the resolve page (100 candidates).
- [ ] Start a database transaction, resolve each `(credential_id, raw_model)` to a binding id, require every binding to belong to the same raw model, update only `manual_priority`, and roll back on any lookup/update failure.
- [ ] Record one audit event containing the model and ordered credential/priorities, then return the updated order.
- [ ] Register the endpoint under `h.superAdmin`; keep the existing single-candidate endpoint unchanged.
- [ ] Add table-driven validation tests for malformed payloads, duplicate/missing priorities, and invalid credential IDs, plus the existing handler registration expectation if applicable.
- [ ] Run `go test ./admin -run 'RoutingCandidateBinding'` and `go test ./...`.

### Task 2: Expose reorder API and normalize resolve candidate contract

**Files:**
- Modify: `web/src/api/routing.ts`
- Modify: `web/src/views/RoutingDashboardView.vue`

- [ ] Add `CandidateBindingReorderItem`, `CandidateBindingReorderRequest`, and `reorderCandidateBindings(rawModel, items)` API types/function.
- [ ] Align `RoutingCandidate` with the backend response by accepting `block_reason` (while retaining compatibility for existing `runtime_block_reason` consumers) and use one display helper in the view.
- [ ] Remove the `showUnavailable` state and computed filter from resolve; `filteredResolveCandidates` becomes the full `resolveCandidates` list or is removed.
- [ ] Add drag state, drag start/over/end handlers, optimistic array reorder, contiguous priority assignment, and rollback on API failure. Only render drag controls and submit reorder for `superAdmin`; non-admin users retain read-only ordering.
- [ ] Pass the canonical raw model used by the resolve response to the reorder API and re-run resolve after successful persistence so backend rank/order is authoritative.
- [ ] Keep unavailable rows visually distinct and include their real block reason.

### Task 3: Update resolve table interaction and styles

**Files:**
- Modify: `web/src/views/RoutingDashboardView.vue`
- Modify: relevant routing locale files only if an existing key is required

- [ ] Add a stable order column and a familiar drag handle icon/text with tooltip/title; do not use a text-only rounded control for the drag affordance.
- [ ] Bind native `draggable`, `dragstart`, `dragover`, `drop`/`dragend`, and a dragging class to table rows.
- [ ] Show every candidate by default, including unavailable candidates, with the current rank and manual priority visible.
- [ ] Remove the obsolete unavailable checkbox and keep the unavailable count in the toolbar summary.
- [ ] Use only registered `--kx-*` tokens from `web/src/style.css` for new styles.

### Task 4: Frontend and backend verification

**Files:**
- No additional source files unless tests expose a contract mismatch.

- [ ] Run `npx vue-tsc --noEmit` and record any pre-existing `SystemMonitorPanel.vue:229` error separately from this feature.
- [ ] Run `npx vite build`.
- [ ] Run the routing candidate binding tests and `go build ./...`.
- [ ] Run the repository kx-token audit and confirm no new unregistered tokens.
- [ ] Review the final diff for auth boundaries, transaction rollback, unavailable-node rendering, and priority ordering behavior.
