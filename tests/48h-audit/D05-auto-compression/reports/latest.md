# R73 · D05 自动压缩续审

结论：Ollama 已接入候选窗口预压缩及一次性 context-length 4xx 内部恢复；httptest 证明第二次请求体变小且成功，context marker 防循环。真实 tokenizer、Redis session cache 与 provider probe 未验证。
