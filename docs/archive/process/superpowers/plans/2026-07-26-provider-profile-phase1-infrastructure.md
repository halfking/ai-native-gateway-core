# Provider Profile System Phase 1: Infrastructure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the foundational infrastructure for the provider profile system including data models, lightweight collector, scoring algorithms, and daily aggregator.

**Architecture:**渐进式演进方案，在现有系统基础上增量扩展。创建新的 domains/providerprofile 包，复用现有的探测框架和数据源。数据流：轻量级采集器(每2小时) → provider_profile_metrics表 → 每日聚合器 → provider_profile_daily表。

**Tech Stack:** Go 1.21+, PostgreSQL 17, pgx/v5, 现有后端调度框架

---

## 前置条件

- [ ] 数据库迁移脚本已执行（`deploy/sql/migrations/2026-07-26-provider-profile-system.sql`）
- [ ] 验证所有表已创建成功

---

## Task 1: 数据模型和类型定义

**Files:**
- Create: `domains/providerprofile/types.go`
- Create: `domains/providerprofile/store.go`

- [ ] **Step 1: 创建基础类型定义**

创建 `domains/providerprofile/types.go`:

```go
// Package providerprofile 实现供应商画像系统
// 通过7个维度评估供应商质量：网络延迟、模型可信度、可用性、稳定性、规模、费用准确性、价格
package providerprofile

import "time"

// ProfileWeights 维度权重配置
type ProfileWeights struct {
	Network      float64 // 0.10 - 网络延迟
	Credibility  float64 // 0.15 - 模型可信度
	Availability float64 // 0.20 - 可用性
	Stability    float64 // 0.20 - 稳定性
	Scale        float64 // 0.05 - 规模
	CostAccuracy float64 // 0.15 - 费用准确性
	Price        float64 // 0.15 - 价格
}

// DefaultWeights 返回默认权重配置
func DefaultWeights() ProfileWeights {
	return ProfileWeights{
		Network:      0.10,
		Credibility:  0.15,
		Availability: 0.20,
		Stability:    0.20,
		Scale:        0.05,
		CostAccuracy: 0.15,
		Price:        0.15,
	}
}

// TimeSlot 时段标识
type TimeSlot string

const (
	TimeSlotDawn      TimeSlot = "dawn"      // 00:00-06:00
	TimeSlotMorning   TimeSlot = "morning"   // 06:00-12:00
	TimeSlotAfternoon TimeSlot = "afternoon" // 12:00-18:00
	TimeSlotEvening   TimeSlot = "evening"   // 18:00-22:00
	TimeSlotNight     TimeSlot = "night"     // 22:00-24:00
)

// DetermineTimeSlot 根据时间确定时段
func DetermineTimeSlot(t time.Time) TimeSlot {
	hour := t.Hour()
	switch {
	case hour < 6:
		return TimeSlotDawn
	case hour < 12:
		return TimeSlotMorning
	case hour < 18:
		return TimeSlotAfternoon
	case hour < 22:
		return TimeSlotEvening
	default:
		return TimeSlotNight
	}
}

// MetricSnapshot 单次采集快照
type MetricSnapshot struct {
	CredentialID int64
	ProviderID   int64
	MetricTime   time.Time
	TimeSlot     TimeSlot

	NetworkMetrics      *NetworkMetrics
	AvailabilityMetrics *AvailabilityMetrics
	StabilityMetrics    *StabilityMetrics
	ScaleMetrics        *ScaleMetrics
}

// NetworkMetrics 网络延迟指标
type NetworkMetrics struct {
	P50 int // ms
	P95 int // ms
	P99 int // ms
}

// AvailabilityMetrics 可用性指标
type AvailabilityMetrics struct {
	TotalRequests   int
	SuccessRequests int
	AvgTTFTMs       int // 首字时间平均值
	AvgDurationMs   int // 完成时长平均值
}

// StabilityMetrics 稳定性指标
type StabilityMetrics struct {
	ErrorCount int
	ErrorTypes map[string]int // {"500": 3, "timeout": 2}
}

// ScaleMetrics 规模指标
type ScaleMetrics struct {
	TotalModels     int
	AvailableModels int
}

// DailyProfile 天级画像数据
type DailyProfile struct {
	ID           int64
	CredentialID int64
	ProviderID   int64
	ProfileDate  time.Time

	// 各维度分数
	NetworkScore      float64
	CredibilityScore  float64
	AvailabilityScore float64
	StabilityScore    float64
	ScaleScore        float64
	CostAccuracyScore float64
	PriceScore        float64
	TotalScore        float64

	// 时段分析
	TimeslotScores map[TimeSlot]float64
	ScoreStddev    float64
	BestTimeslot   TimeSlot
	WorstTimeslot  TimeSlot

	// 原始数据（JSONB）
	RawStats map[string]interface{}

	CreatedAt time.Time
}
```

- [ ] **Step 2: 运行 gofmt**

```bash
gofmt -w domains/providerprofile/types.go
```

Expected: 代码格式化成功

- [ ] **Step 3: 创建数据存储接口**

创建 `domains/providerprofile/store.go`:

```go
package providerprofile

import (
	"context"
	"time"
)

// MetricsStore 指标数据存储接口
type MetricsStore interface {
	// SaveSnapshot 保存采集快照
	SaveSnapshot(ctx context.Context, snapshot *MetricSnapshot) error
	
	// GetSnapshotsByDateRange 获取指定时间范围的快照
	GetSnapshotsByDateRange(ctx context.Context, credentialID int64, start, end time.Time) ([]*MetricSnapshot, error)
	
	// CleanupOldMetrics 清理过期数据（7天前）
	CleanupOldMetrics(ctx context.Context) (int64, error)
}

// ProfileStore 画像数据存储接口
type ProfileStore interface {
	// SaveDailyProfile 保存天级画像
	SaveDailyProfile(ctx context.Context, profile *DailyProfile) error
	
	// GetDailyProfile 获取指定日期的画像
	GetDailyProfile(ctx context.Context, credentialID int64, date time.Time) (*DailyProfile, error)
	
	// GetRecentProfiles 获取最近N天的画像
	GetRecentProfiles(ctx context.Context, credentialID int64, days int) ([]*DailyProfile, error)
	
	// GetProfilesByProvider 获取供应商所有credential的最新画像
	GetProfilesByProvider(ctx context.Context, providerID int64) ([]*DailyProfile, error)
}
```

- [ ] **Step 4: 运行 gofmt**

```bash
gofmt -w domains/providerprofile/store.go
```

- [ ] **Step 5: 提交代码**

```bash
git add domains/providerprofile/types.go domains/providerprofile/store.go
git commit -m "feat(providerprofile): add data models and store interfaces"
```

---

## Task 2: PostgreSQL存储实现

**Files:**
- Create: `domains/providerprofile/pg_store.go`
- Create: `domains/providerprofile/pg_store_test.go`

- [ ] **Step 1: 编写测试 - MetricsStore.SaveSnapshot**

创建 `domains/providerprofile/pg_store_test.go`:

```go
package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	
	// 使用测试数据库连接
	connString := "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable"
	pool, err := pgxpool.New(context.Background(), connString)
	require.NoError(t, err)
	
	t.Cleanup(func() {
		pool.Close()
	})
	
	return pool
}

func TestPGMetricsStore_SaveSnapshot(t *testing.T) {
	pool := setupTestDB(t)
	store := providerprofile.NewPGMetricsStore(pool)
	
	snapshot := &providerprofile.MetricSnapshot{
		CredentialID: 1,
		ProviderID:   1,
		MetricTime:   time.Now(),
		TimeSlot:     providerprofile.TimeSlotMorning,
		NetworkMetrics: &providerprofile.NetworkMetrics{
			P50: 100,
			P95: 200,
			P99: 300,
		},
		AvailabilityMetrics: &providerprofile.AvailabilityMetrics{
			TotalRequests:   100,
			SuccessRequests: 95,
			AvgTTFTMs:       500,
			AvgDurationMs:   2000,
		},
		StabilityMetrics: &providerprofile.StabilityMetrics{
			ErrorCount: 5,
			ErrorTypes: map[string]int{"500": 3, "timeout": 2},
		},
		ScaleMetrics: &providerprofile.ScaleMetrics{
			TotalModels:     10,
			AvailableModels: 9,
		},
	}
	
	err := store.SaveSnapshot(context.Background(), snapshot)
	assert.NoError(t, err)
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./domains/providerprofile -v -run TestPGMetricsStore_SaveSnapshot
```

Expected: FAIL - NewPGMetricsStore 函数未定义

- [ ] **Step 3: 实现 PGMetricsStore**

创建 `domains/providerprofile/pg_store.go`:

```go
package providerprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGMetricsStore PostgreSQL指标存储实现
type PGMetricsStore struct {
	db *pgxpool.Pool
}

// NewPGMetricsStore 创建PostgreSQL指标存储
func NewPGMetricsStore(db *pgxpool.Pool) *PGMetricsStore {
	return &PGMetricsStore{db: db}
}

// SaveSnapshot 保存采集快照
func (s *PGMetricsStore) SaveSnapshot(ctx context.Context, snapshot *MetricSnapshot) error {
	errorTypesJSON, err := json.Marshal(snapshot.StabilityMetrics.ErrorTypes)
	if err != nil {
		return fmt.Errorf("marshal error types: %w", err)
	}

	query := `
		INSERT INTO provider_profile_metrics (
			credential_id, provider_id, metric_time, time_slot,
			network_latency_p50, network_latency_p95, network_latency_p99,
			availability_total_requests, availability_success_requests,
			availability_ttft_avg_ms, availability_duration_avg_ms,
			stability_error_count, stability_error_types,
			scale_total_models, scale_available_models
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`

	_, err = s.db.Exec(ctx, query,
		snapshot.CredentialID,
		snapshot.ProviderID,
		snapshot.MetricTime,
		snapshot.TimeSlot,
		snapshot.NetworkMetrics.P50,
		snapshot.NetworkMetrics.P95,
		snapshot.NetworkMetrics.P99,
		snapshot.AvailabilityMetrics.TotalRequests,
		snapshot.AvailabilityMetrics.SuccessRequests,
		snapshot.AvailabilityMetrics.AvgTTFTMs,
		snapshot.AvailabilityMetrics.AvgDurationMs,
		snapshot.StabilityMetrics.ErrorCount,
		errorTypesJSON,
		snapshot.ScaleMetrics.TotalModels,
		snapshot.ScaleMetrics.AvailableModels,
	)

	if err != nil {
		return fmt.Errorf("insert metrics snapshot: %w", err)
	}

	return nil
}

// GetSnapshotsByDateRange 获取指定时间范围的快照
func (s *PGMetricsStore) GetSnapshotsByDateRange(ctx context.Context, credentialID int64, start, end time.Time) ([]*MetricSnapshot, error) {
	query := `
		SELECT 
			credential_id, provider_id, metric_time, time_slot,
			network_latency_p50, network_latency_p95, network_latency_p99,
			availability_total_requests, availability_success_requests,
			availability_ttft_avg_ms, availability_duration_avg_ms,
			stability_error_count, stability_error_types,
			scale_total_models, scale_available_models
		FROM provider_profile_metrics
		WHERE credential_id = $1
		  AND metric_time >= $2
		  AND metric_time < $3
		ORDER BY metric_time ASC
	`

	rows, err := s.db.Query(ctx, query, credentialID, start, end)
	if err != nil {
		return nil, fmt.Errorf("query snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []*MetricSnapshot
	for rows.Next() {
		var snapshot MetricSnapshot
		var timeSlotStr string
		var errorTypesJSON []byte

		snapshot.NetworkMetrics = &NetworkMetrics{}
		snapshot.AvailabilityMetrics = &AvailabilityMetrics{}
		snapshot.StabilityMetrics = &StabilityMetrics{}
		snapshot.ScaleMetrics = &ScaleMetrics{}

		err := rows.Scan(
			&snapshot.CredentialID,
			&snapshot.ProviderID,
			&snapshot.MetricTime,
			&timeSlotStr,
			&snapshot.NetworkMetrics.P50,
			&snapshot.NetworkMetrics.P95,
			&snapshot.NetworkMetrics.P99,
			&snapshot.AvailabilityMetrics.TotalRequests,
			&snapshot.AvailabilityMetrics.SuccessRequests,
			&snapshot.AvailabilityMetrics.AvgTTFTMs,
			&snapshot.AvailabilityMetrics.AvgDurationMs,
			&snapshot.StabilityMetrics.ErrorCount,
			&errorTypesJSON,
			&snapshot.ScaleMetrics.TotalModels,
			&snapshot.ScaleMetrics.AvailableModels,
		)
		if err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}

		snapshot.TimeSlot = TimeSlot(timeSlotStr)

		if len(errorTypesJSON) > 0 {
			if err := json.Unmarshal(errorTypesJSON, &snapshot.StabilityMetrics.ErrorTypes); err != nil {
				return nil, fmt.Errorf("unmarshal error types: %w", err)
			}
		}

		snapshots = append(snapshots, &snapshot)
	}

	return snapshots, rows.Err()
}

// CleanupOldMetrics 清理7天前的数据
func (s *PGMetricsStore) CleanupOldMetrics(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM provider_profile_metrics
		WHERE created_at < NOW() - INTERVAL '7 days'
	`

	result, err := s.db.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("cleanup old metrics: %w", err)
	}

	return result.RowsAffected(), nil
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./domains/providerprofile -v -run TestPGMetricsStore_SaveSnapshot
```

Expected: PASS

- [ ] **Step 5: 提交代码**

```bash
git add domains/providerprofile/pg_store.go domains/providerprofile/pg_store_test.go
git commit -m "feat(providerprofile): implement PostgreSQL metrics store"
```

---

由于完整的实施计划非常长（预计超过5000行），我将其分为多个文件。这是第一阶段的前两个任务。

**完整计划包含**:
- Task 1-2: 数据模型和存储（已完成）
- Task 3-5: 评分算法实现
- Task 6-8: 轻量级采集器
- Task 9-11: 每日聚合器
- Task 12-14: 定时任务集成
- Task 15: 集成测试

是否需要我继续完成剩余的任务？还是先审查这两个任务的设计？
