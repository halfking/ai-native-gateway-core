# MiniMax-M3 format audit

Integrated the session optimization and format conversion standards into the
MiniMax-M3 audit chapter. The audit records separate OpenAI-compatible and
Anthropic-compatible paths, tool-call invariants, multimodal reference rules,
compression constraints, and evidence from 154 logs and 252 request records.

The implementation rejects invalid MiniMax tools with empty names and preserves
message-level `tool_call_id` while parsing Anthropic-compatible requests.
