package analysis

import "time"

// SessionSummary 单会话摘要。
//
// 由 TopicSummarizer Worker 在 session 关闭或空闲超阈值时产出。
type SessionSummary struct {
	SessionID   string
	TenantID    string
	Title       string
	Summary     string
	KeyPoints   []string
	Quality     int
	GeneratedAt time.Time
	ModelID     string
	WorkerName  string
}

// MultiSessionCluster 多会话聚类结果。
//
// 由 MultiSessionClusterer 在批处理窗口内产出；用于企业级会话资产归纳。
type MultiSessionCluster struct {
	ClusterID   string
	TenantID    string
	TopicPath   []string
	SessionIDs  []string
	MemberCount int
	Centroid    string
	GeneratedAt time.Time
}
