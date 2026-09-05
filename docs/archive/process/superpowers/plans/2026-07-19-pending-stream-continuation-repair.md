# Pending Stream Continuation Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair client-disconnect upstream continuation so protocol bridges have one reader, pending replay is tenant-safe and complete, and stream deadlines are coherent.

**Architecture:** Preserve existing gateway bridge wiring and pending store APIs. Make executor dispatch mutually exclusive, preserve request values with `context.WithoutCancel`, make pending persistence fail closed, and convert capture overflow into a terminal failed state.

**Tech Stack:** Go, `net/http`, `context`, Redis/go-redis, `httptest`.

---

- [ ] Enforce one stream reader for Q2 and Responses bridge dispatch; remove the obsolete Messages wrapper; add Q2 and Anthropic-to-Responses disconnect capture tests.
- [ ] Preserve tenant values through detached upstream/async contexts; require tenant on all pending writes; add detached-context and same-tenant replay tests.
- [ ] Fail closed for public and admin pending access when session ownership or tenant scope cannot be proven.
- [ ] Add a dedicated 300-second pending TTL configuration and reject capturer overflow as `pending_capture_overflow`.
- [ ] Remove global 120-second client deadlines from routed stream-capable clients; close and drain Anthropic bridge reads on chunk timeout.
- [ ] Run formatting, focused tests, race tests, vet, full tests, a final code audit, then commit and push only repair files.
