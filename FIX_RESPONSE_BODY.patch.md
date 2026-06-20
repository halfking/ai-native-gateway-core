# 修复 ResponseBody 不保存的问题

## 修改 1: routing/executor_anthropic.go

### 修改函数签名（第 97 行）

**修改前**:
```go
func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) error {
```

**修改后**:
```go
func (a *AnthropicExecutor) WriteNonStreamResponse(w http.ResponseWriter, resp *http.Response, clientModel, qualityFixMode string, qualitySignals *QualitySignals) ([]byte, error) {
```

### 修改返回语句（第 162 和 186 行）

**修改前**（第 162 行）:
```go
	_, err = w.Write(body)
	return err
```

**修改后**:
```go
	_, err = w.Write(body)
	return body, err
```

**修改前**（第 186 行）:
```go
	_, err = w.Write(body)
	return err
```

**修改后**:
```go
	_, err = w.Write(body)
	return body, err
```

### 修改调用方（第 704-720 行）

**修改前**:
```go
var qualitySignals QualitySignals
if err := ae.WriteNonStreamResponse(params.W, resp, params.ClientModel, cand.QualityFixMode, &qualitySignals); err != nil {
	return nil, err
}
return &ExecuteResult{
	Response:    resp,
	Candidate:   cand,
	LatencyMs:   latencyMs,
	RequestBody: append([]byte(nil), bodyBytes...),
	QualityFlags:      qualitySignals.Flags,
	QualityFixActions: qualitySignals.FixActions,
	QualityScore:      qualitySignals.Score,
}, nil
```

**修改后**:
```go
var qualitySignals QualitySignals
responseBody, err := ae.WriteNonStreamResponse(params.W, resp, params.ClientModel, cand.QualityFixMode, &qualitySignals)
if err != nil {
	return nil, err
}
return &ExecuteResult{
	Response:    resp,
	Candidate:   cand,
	LatencyMs:   latencyMs,
	RequestBody: append([]byte(nil), bodyBytes...),
	ResponseBody: responseBody, // ✅ 添加响应体
	QualityFlags:      qualitySignals.Flags,
	QualityFixActions: qualitySignals.FixActions,
	QualityScore:      qualitySignals.Score,
}, nil
```

---

## 修改 2: routing/executor_chat.go (ChatExecutor 同样的问题)

需要查看 ChatExecutor 是否有同样的问题并做相同的修复。

---

## 修改 3: routing/protocol_handler_test.go (测试代码)

更新测试桩以匹配新的接口。

