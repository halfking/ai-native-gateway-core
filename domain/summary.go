package domain

// SummaryContext 封装请求总结/日志聚合领域的上下文。
type SummaryContext struct {
	IncludeRequestBody  bool
	IncludeResponseBody bool
	RedactPatterns      []string
	MaxBodyBytes        int
	Truncate            bool
}
