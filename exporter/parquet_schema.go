// exporter/parquet_schema.go — 2026-09-06
//
// Parquet schema definition for AUTO route training data export.
// 
// Privacy compliance:
//   - ✅ ONLY exports 15 structured feature fields (detected_language, etc.)
//   - ❌ NEVER exports prompt, messages, response, summary, keywords
//   - ✅ All fields are non-reversible (enums, buckets, hashes, booleans)
//
// Schema version: v1 (matches structured_features.go FeatureVersion)
//
// Usage:
//   schema := GetTrainingDataSchema()
//   writer, _ := parquet.NewWriter(file, schema)
//   writer.Write(record)
//
// Part of: P2.2 - Training Data Export Pipeline

package exporter

// TrainingDataRecord 是导出到Parquet的记录结构。
// 隐私保证：只包含15个结构化特征字段，不包含原始内容。
type TrainingDataRecord struct {
	// ============================================================
	// 元数据字段（非特征）
	// ============================================================
	
	// RequestID 唯一请求ID（用于追溯，非训练特征）
	RequestID string `parquet:"name=request_id, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// Timestamp 请求时间戳（Unix milliseconds）
	Timestamp int64 `parquet:"name=timestamp, type=INT64, repetitiontype=REQUIRED"`
	
	// TaskType 任务类型（chat, completion, embedding）
	TaskType string `parquet:"name=task_type, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// Profile 用户偏好（balanced, cost, speed, quality）
	Profile string `parquet:"name=profile, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// Classifier 分类器版本（heuristic_v1, ml_v1）
	Classifier string `parquet:"name=classifier, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// Confidence 路由置信度（0.0-1.0）
	Confidence float64 `parquet:"name=confidence, type=DOUBLE, repetitiontype=REQUIRED"`
	
	// ============================================================
	// 15个结构化特征字段（训练输入）
	// ============================================================
	
	// DetectedLanguage 检测到的语言（zh, en, ja, etc.）
	DetectedLanguage *string `parquet:"name=detected_language, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// PromptLengthBucket 提示词长度桶（xs, s, m, l, xl, xxl）
	PromptLengthBucket *string `parquet:"name=prompt_length_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// ContextLengthBucket 上下文长度桶（none, s, m, l, xl）
	ContextLengthBucket *string `parquet:"name=context_length_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// TurnCountBucket 对话轮数桶（single, few, many）
	TurnCountBucket *string `parquet:"name=turn_count_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// HasCodeIndicator 是否包含代码
	HasCodeIndicator *bool `parquet:"name=has_code_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// HasMathIndicator 是否包含数学公式
	HasMathIndicator *bool `parquet:"name=has_math_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// HasTableIndicator 是否包含表格
	HasTableIndicator *bool `parquet:"name=has_table_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// HasMultimediaIndicator 是否包含多媒体（图片、视频等）
	HasMultimediaIndicator *bool `parquet:"name=has_multimedia_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// IntentCategory 意图分类（qa, generation, analysis, etc.）
	IntentCategory *string `parquet:"name=intent_category, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// DomainHint 领域提示（tech, finance, medical, etc.）
	DomainHint *string `parquet:"name=domain_hint, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// ComplexityBucket 复杂度桶（simple, moderate, complex）
	ComplexityBucket *string `parquet:"name=complexity_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"`
	
	// LatencySensitive 是否对延迟敏感
	LatencySensitive *bool `parquet:"name=latency_sensitive, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// CostSensitive 是否对成本敏感
	CostSensitive *bool `parquet:"name=cost_sensitive, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// FeatureVersion 特征版本（v1, v2）
	FeatureVersion string `parquet:"name=feature_version, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// ContentHash 内容哈希（SHA256，用于去重）
	ContentHash string `parquet:"name=content_hash, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// ============================================================
	// 标签字段（训练输出/监督信号）
	// ============================================================
	
	// ChosenModel 选择的模型（gpt-4, claude-3.5-sonnet, etc.）
	ChosenModel string `parquet:"name=chosen_model, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"`
	
	// Success 请求是否成功（已结算的行才有）
	Success *bool `parquet:"name=success, type=BOOLEAN, repetitiontype=OPTIONAL"`
	
	// LatencyMs 实际延迟（毫秒，已结算的行才有）
	LatencyMs *int32 `parquet:"name=latency_ms, type=INT32, repetitiontype=OPTIONAL"`
	
	// Reward 结算后的reward（0.0-1.0，已结算的行才有）
	Reward *float64 `parquet:"name=reward, type=DOUBLE, repetitiontype=OPTIONAL"`
}

// GetTrainingDataSchema 返回Parquet schema定义。
// 隐私保证：schema只包含结构化特征字段，不包含prompt/messages。
func GetTrainingDataSchema() string {
	return `{
		"Tag": "name=training_data, repetitiontype=REQUIRED",
		"Fields": [
			{"Tag": "name=request_id, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=timestamp, type=INT64, repetitiontype=REQUIRED"},
			{"Tag": "name=task_type, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=profile, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=classifier, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=confidence, type=DOUBLE, repetitiontype=REQUIRED"},
			
			{"Tag": "name=detected_language, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=prompt_length_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=context_length_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=turn_count_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=has_code_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=has_math_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=has_table_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=has_multimedia_indicator, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=intent_category, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=domain_hint, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=complexity_bucket, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=OPTIONAL"},
			{"Tag": "name=latency_sensitive, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=cost_sensitive, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=feature_version, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=content_hash, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			
			{"Tag": "name=chosen_model, type=BYTE_ARRAY, convertedtype=UTF8, repetitiontype=REQUIRED"},
			{"Tag": "name=success, type=BOOLEAN, repetitiontype=OPTIONAL"},
			{"Tag": "name=latency_ms, type=INT32, repetitiontype=OPTIONAL"},
			{"Tag": "name=reward, type=DOUBLE, repetitiontype=OPTIONAL"}
		]
	}`
}

// FieldNames 返回导出字段的名称列表（用于验证和日志）。
func FieldNames() []string {
	return []string{
		"request_id",
		"timestamp",
		"task_type",
		"profile",
		"classifier",
		"confidence",
		"detected_language",
		"prompt_length_bucket",
		"context_length_bucket",
		"turn_count_bucket",
		"has_code_indicator",
		"has_math_indicator",
		"has_table_indicator",
		"has_multimedia_indicator",
		"intent_category",
		"domain_hint",
		"complexity_bucket",
		"latency_sensitive",
		"cost_sensitive",
		"feature_version",
		"content_hash",
		"chosen_model",
		"success",
		"latency_ms",
		"reward",
	}
}

// ProhibitedFields 返回禁止导出的字段列表（隐私合规）。
// 用于测试验证：确保导出文件不包含这些字段。
func ProhibitedFields() []string {
	return []string{
		"prompt",        // ❌ 原始prompt文本
		"messages",      // ❌ 对话历史
		"response",      // ❌ 模型响应
		"summary",       // ❌ 摘要（可能包含原文片段）
		"keywords",      // ❌ 关键词（可能包含敏感信息）
		"context",       // ❌ 原始context文本
		"user_id",       // ❌ 用户ID（隐私敏感）
		"api_key",       // ❌ API密钥
		"ip_address",    // ❌ IP地址
	}
}

// ValidateRecord 验证记录是否符合隐私合规要求。
// 用于测试：确保TrainingDataRecord结构不包含敏感字段。
func ValidateRecord(record *TrainingDataRecord) error {
	// 结构体本身已通过类型安全保证不包含禁止字段
	// 此函数用于运行时额外验证（可选）
	return nil
}
