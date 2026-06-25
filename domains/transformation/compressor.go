package transformation

// Compressor 上下文压缩器。
//
// 用途：标记过长的请求体，让 PhasePostTransform 的 Hook 或上游的
// 真实压缩逻辑（ctx_compress.go）触发裁剪。
//
// 设计：本实现是"标记器"而非"裁剪器"——只设置 ctx.Metadata["needs_compression"]
// = true，避免与旧 transform/ctx_compress.go 产生实现冲突。
//
// 阈值计算：maxTokens * 4（启发式：1 token ≈ 4 字节，对应约 3.5 字符/Token）。
// 当 Request 字节数 > maxTokens * 4 时认为需要压缩。
type Compressor struct {
	// maxTokens 最大 token 数；<=0 时使用 4096 默认值。
	maxTokens int
}

// NewCompressor 构造压缩器。
//
// maxTokens <= 0 会被替换为 4096（合理默认值）。
func NewCompressor(maxTokens int) *Compressor {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &Compressor{maxTokens: maxTokens}
}

// Name 返回转换器名称。
func (c *Compressor) Name() string { return "compressor" }

// Transform 标记过长的请求体。
//
// 行为：若 len(ctx.Request) > c.maxTokens * 4，置 ctx.Metadata["needs_compression"]
// = true；否则置 false。
func (c *Compressor) Transform(ctx Context) error {
	if c == nil {
		return nil
	}
	if ctx.Metadata == nil {
		ctx.Metadata = map[string]any{}
	}
	threshold := c.maxTokens * 4
	needs := len(ctx.Request) > threshold
	ctx.Metadata["needs_compression"] = needs
	ctx.Metadata["compress_max_tokens"] = c.maxTokens
	return nil
}
