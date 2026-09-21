// annotation/types.go — 2026-09-06
//
// P2.1人工标注工作流核心类型定义
//
// 功能:
//   - 定义标注记录数据结构
//   - 定义CSV导出/导入格式
//   - 定义统计数据结构
//
// Privacy:
//   - ✅ 标注记录不包含prompt/messages/response
//   - ✅ 只存储结构化特征和provider标签

package annotation

import "time"

// AnnotationRecord 表示一条人工标注记录
type AnnotationRecord struct {
	ID               int64     `db:"id"`
	RequestID        string    `db:"request_id"`
	AutoLabel        string    `db:"auto_label"`
	AutoConfidence   float64   `db:"auto_confidence"`
	HumanLabel       string    `db:"human_label"`
	IsCorrect        bool      `db:"is_correct"`
	AnnotationReason *string   `db:"annotation_reason"`
	Annotator        string    `db:"annotator"`
	AnnotatedAt      time.Time `db:"annotated_at"`
	CreatedAt        time.Time `db:"created_at"`
}

// CSVAnnotationRow 表示CSV文件中的一行（用于导出和导入）
type CSVAnnotationRow struct {
	// 请求标识
	RequestID string `csv:"request_id"`
	
	// 请求特征（从auto_route_selections获取）
	ModelName      string  `csv:"model_name"`
	TaskType       string  `csv:"task_type"`
	PromptTokens   int     `csv:"prompt_tokens"`
	IsStreaming    bool    `csv:"is_streaming"`
	HasVision      bool    `csv:"has_vision"`
	Region         string  `csv:"region"`
	Profile        string  `csv:"profile"`
	
	// ML预测结果
	AutoProvider   string  `csv:"auto_provider"`
	Confidence     float64 `csv:"confidence"`
	
	// 人工标注（导入时必填）
	HumanProvider  string  `csv:"human_provider"`
	IsCorrect      string  `csv:"is_correct"`      // TRUE/FALSE/true/false
	Reason         string  `csv:"reason"`          // performance/cost/availability/quality/other
	Annotator      string  `csv:"annotator"`
}

// AnnotationStats 标注统计数据
// JSON keys are snake_case to match web/src/api/annotations.ts; the
// timestamp pointers are null when no annotations exist yet.
type AnnotationStats struct {
	TotalAnnotations  int        `db:"total_annotations" json:"total_annotations"`
	CorrectCount      int        `db:"correct_count" json:"correct_count"`
	IncorrectCount    int        `db:"incorrect_count" json:"incorrect_count"`
	AccuracyPercent   float64    `db:"accuracy_percent" json:"accuracy_percent"`
	NumAnnotators     int        `db:"num_annotators" json:"num_annotators"`
	FirstAnnotationAt *time.Time `db:"first_annotation_at" json:"first_annotation_at"`
	LastAnnotationAt  *time.Time `db:"last_annotation_at" json:"last_annotation_at"`
}

// ProviderAccuracy 分provider准确率统计
type ProviderAccuracy struct {
	Provider             string  `db:"provider" json:"provider"`
	TotalPredictions     int     `db:"total_predictions" json:"total_predictions"`
	CorrectPredictions   int     `db:"correct_predictions" json:"correct_predictions"`
	IncorrectPredictions int     `db:"incorrect_predictions" json:"incorrect_predictions"`
	AccuracyPercent      float64 `db:"accuracy_percent" json:"accuracy_percent"`
	AvgConfidence        float64 `db:"avg_confidence" json:"avg_confidence"`
}

// AnnotatorStats 标注人员统计
type AnnotatorStats struct {
	Annotator          string    `db:"annotator" json:"annotator"`
	TotalAnnotations   int       `db:"total_annotations" json:"total_annotations"`
	CorrectCount       int       `db:"correct_count" json:"correct_count"`
	IncorrectCount     int       `db:"incorrect_count" json:"incorrect_count"`
	AccuracyPercent    float64   `db:"accuracy_percent" json:"accuracy_percent"`
	FirstAnnotationAt  time.Time `db:"first_annotation_at" json:"first_annotation_at"`
	LastAnnotationAt   time.Time `db:"last_annotation_at" json:"last_annotation_at"`
	HoursSpan          float64   `db:"hours_span" json:"hours_span"`
}

// ReasonDistribution 标注原因分布
type ReasonDistribution struct {
	Reason  string  `db:"annotation_reason" json:"reason"`
	Count   int     `db:"count" json:"count"`
	Percent float64 `db:"percent" json:"percentage"`
}

// ExportConfig 导出配置
type ExportConfig struct {
	// 时间范围
	StartDate time.Time
	EndDate   time.Time
	
	// 质量过滤
	MinConfidence *float64
	MaxConfidence *float64
	
	// 数量限制
	Limit int
	
	// 输出路径
	OutputPath string
}

// ImportResult 导入结果
type ImportResult struct {
	TotalRows       int
	SuccessRows     int
	SkippedRows     int
	ErrorRows       int
	Errors          []ImportError
	DurationSeconds int
}

// ImportError 导入错误
type ImportError struct {
	Row     int
	Line    string
	Message string
}

// ValidateResult CSV验证结果
type ValidateResult struct {
	IsValid         bool
	TotalRows       int
	ValidRows       int
	InvalidRows     int
	Errors          []ValidationError
}

// ValidationError 验证错误
type ValidationError struct {
	Row     int
	Field   string
	Value   string
	Message string
}

// AnnotationReason 标注原因枚举
type AnnotationReason string

const (
	ReasonPerformance  AnnotationReason = "performance"
	ReasonCost         AnnotationReason = "cost"
	ReasonAvailability AnnotationReason = "availability"
	ReasonQuality      AnnotationReason = "quality"
	ReasonOther        AnnotationReason = "other"
	ReasonCorrect      AnnotationReason = "correct" // auto_label正确
)

// ValidAnnotationReasons 返回有效的标注原因列表
func ValidAnnotationReasons() []string {
	return []string{
		string(ReasonPerformance),
		string(ReasonCost),
		string(ReasonAvailability),
		string(ReasonQuality),
		string(ReasonOther),
		string(ReasonCorrect),
	}
}

// IsValidReason 检查标注原因是否有效
func IsValidReason(reason string) bool {
	for _, valid := range ValidAnnotationReasons() {
		if reason == valid {
			return true
		}
	}
	return false
}
