package domain

// CompressionContext 封装压缩/解压缩领域的上下文。
type CompressionContext struct {
	RequestEncoding  string
	ResponseEncoding string
	CompressUpstream bool
	CompressClient   bool
	MinSizeBytes     int
}
