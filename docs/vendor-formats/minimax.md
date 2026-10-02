# MiniMax 方言规范

## 概述

- Base protocol: `openai-chat`（兼容模式）
- 方言: `DialectMiniMax`
- 官方文档: https://platform.MiniMax.io/
- 端点: `POST /v1/text/chatcompletion_v2`
- catalog code: `MiniMax`（实际字符串"minimax"）

## 与 OpenAI 的差异

### 1. 错误信号 `base_resp`（P0-MiniMax-1 已修复）

MiniMax 在 HTTP 200 响应里也会通过 `base_resp.status_code` 返回错误：

```json
{
  "base_resp": {
    "status_code": 1004,
    "status_msg": "rate limit exceeded"
  },
  "choices": [...]
}
```

`internal/ir/parse_openai.go` 在 strip 字段前先解析 `base_resp.status_code`：
- 非零 → 视为上游错误
- 错误类型通过 `status_code` 数值区间分类

流式响应同样会检查 `base_resp`，若发现错误则中断流并返回分类后的错误。

### 2. 内容审核字段

```json
{
  "choices": [{
    "message": {
      "role": "assistant",
      "content": "...",
      "input_sensitive_type": 0,
      "output_sensitive_type": 0
    }
  }]
}
```

`input_sensitive_type` / `output_sensitive_type`：
- `0`：正常
- `1`：疑似敏感
- `2`：高置信敏感

**当前实现**：被 strip 删除（细粒度审核信号丢失）。后续 P2-MiniMax-2 计划新增 `IR.ContentSafety` 结构。

### 3. 推理内容 `reasoning_content`

```json
{
  "choices": [{
    "message": {
      "reasoning_content": "思考..."
    }
  }]
}
```

### 4. 私有参数 `mask_sensitive_info`

```json
{"mask_sensitive_info": true}
```

控制 MiniMax 是否对响应内容做脱敏处理。

### 5. bot 类型选择

```json
{"bot_setting": [
  {"bot_name": "MM Smart Expert", "content": "你是 MiniMax 的专家助手..."}
]}
```

可指定多个 bot 角色。

### 6. function_call 格式

与 OpenAI 一致。

### 7. 错误码

`base_resp.status_code` 取值：
- `0`：成功
- `1002`：限流
- `1003`：参数错误
- `1004`：频率过高
- `1008`：余额不足
- `1027`：内容超长
- `1039`：token 超限
- `2056`：模型过载

## IR 映射

| MiniMax 字段 | IR 字段 |
|--------------|---------|
| `base_resp.status_code != 0` | 触发 `UpstreamError` |
| `reasoning_content` | `Response.ReasoningContent` |
| `mask_sensitive_info` | `Request.MaskSensitiveInfo` |
| `bot_setting` | `Request.BotSetting` |
| `input_sensitive_type` | 当前 strip，后续映射 `ContentSafety.InputType` |
| `output_sensitive_type` | 当前 strip，后续映射 `ContentSafety.OutputType` |

## 序列化注意

1. `base_resp.status_code` 必须**先于** strip 检查，因为 MiniMax 错误可能与正常 `choices` 并存。
2. `mask_sensitive_info` 仅在目标方言是 MiniMax 时序列化。
3. `bot_setting` 在某些场景下替代 system message。
