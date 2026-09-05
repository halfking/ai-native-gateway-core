# Format conversion audit

Added the `docs/格式转换/` documentation set covering client protocols,
provider adapters, multimodal mappings, audit status, simulation fixtures and
release gates. Corrected native Gemini serialization so OpenAI-shaped IR tool
calls and tool results become Gemini `functionCall` and `functionResponse`
parts without being dropped.
