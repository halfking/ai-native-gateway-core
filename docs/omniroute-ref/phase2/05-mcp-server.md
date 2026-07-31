# M1: MCP Server

> Status: `RECONSTRUCTED-DRAFT`
> OmniRoute registry facts are `SOURCE-VERIFIED`; the Go server is `NEW-DESIGN`.

## Source facts

- `MCP_TOOLS` in `open-sse/mcp-server/schemas/tools.ts` currently contains 42 registry entries: 34 declared in that file, 6 CCR tools, `toolSearchTool`, and `pickFastestModelTool`.
- The server unions and de-duplicates additional memory, skill, agent, GitHub, pool, plugin, Notion, Obsidian, local-corpus, and compression collections. Therefore 42 is not the server's total visible tool count.
- Scope authorization is metadata/evaluator driven; a fixed claim such as “30 scopes” is not a current source fact.
- `toolCardinality.ts` contains counting logic, but its live registration loop is unchanged; cardinality is not fully wired into registration.

## Proposed Go boundary

`NEW-DESIGN`: a JSON-RPC dispatcher, HTTP/SSE transport, tool metadata registry, tenant-aware policy adapter, and audit sink. Reuse `registry.ToolRegistry.IsAllowed` and existing gateway authentication. Never accept tenant identity from tool arguments. PG/RLS, audit retention, transport limits, and cancellation require separate review.

## Gates

Contract tests for initialize/list/call/error, tenant isolation tests, duplicate-tool tests, bounded tool payloads, audit failure behavior, and race tests. The first implementation must be independently disableable and must not change the existing chat tool path.
