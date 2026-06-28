package analysis

import "time"

// Topic 主题标签。
//
// 来自 TopicSummarizer / MultiSessionClusterer 的产出。
type Topic struct {
	Name   string
	Weight float64
	Source string
}

// TopicNode 主题树节点。
//
// 主题树按 (tenant_id, path) 路径表达；Children 可为 nil 表示叶子。
type TopicNode struct {
	Name     string
	Path     []string
	Count    int
	Children []*TopicNode
}

// TopicAssignment 会话到主题路径的归属。
//
// 一个会话可被分配到多个 TopicAssignment（按 score 排序）。
type TopicAssignment struct {
	SessionID  string
	TenantID   string
	Path       []string
	Score      float64
	AssignedAt time.Time
}
