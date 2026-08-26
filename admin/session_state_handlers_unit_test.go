package admin

import (
	"sort"
	"testing"
)

// 这个文件包含独立的单元测试，不依赖外部组件

// testFilterByHealthGrade 测试健康等级过滤功能（独立函数）
func testFilterByHealthGrade(items []sessionListItem, gradeFilter string) []sessionListItem {
	if gradeFilter == "" {
		return items
	}

	grades := make([]string, 0)
	for _, g := range gradeFilter {
		if g != ',' && g != ' ' {
			grades = append(grades, string(g))
		}
	}
	
	gradeSet := make(map[string]bool)
	for _, g := range grades {
		gradeSet[g] = true
	}

	filtered := make([]sessionListItem, 0, len(items))
	for _, item := range items {
		if item.HealthGrade == nil {
			filtered = append(filtered, item)
			continue
		}
		if gradeSet[*item.HealthGrade] {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// testStatusPriority 状态优先级函数（用于测试）
func testStatusPriority(status string) int {
	switch status {
	case "error":
		return 1
	case "waiting":
		return 2
	case "active":
		return 3
	case "stopped":
		return 4
	case "recovered":
		return 5
	default:
		return 6
	}
}

// testSortSessionList 排序函数（独立）
func testSortSessionList(items []sessionListItem, sortBy string) {
	switch sortBy {
	case "health":
		sort.Slice(items, func(i, j int) bool {
			if testStatusPriority(items[i].Status) != testStatusPriority(items[j].Status) {
				return testStatusPriority(items[i].Status) < testStatusPriority(items[j].Status)
			}
			scoreI := 0
			if items[i].HealthScore != nil {
				scoreI = *items[i].HealthScore
			}
			scoreJ := 0
			if items[j].HealthScore != nil {
				scoreJ = *items[j].HealthScore
			}
			return scoreI > scoreJ
		})
	case "cost":
		sort.Slice(items, func(i, j int) bool {
			if testStatusPriority(items[i].Status) != testStatusPriority(items[j].Status) {
				return testStatusPriority(items[i].Status) < testStatusPriority(items[j].Status)
			}
			return items[i].TotalCostUSD > items[j].TotalCostUSD
		})
	default:
		sort.Slice(items, func(i, j int) bool {
			if testStatusPriority(items[i].Status) != testStatusPriority(items[j].Status) {
				return testStatusPriority(items[i].Status) < testStatusPriority(items[j].Status)
			}
			scoreI := 100
			if items[i].HealthScore != nil {
				scoreI = *items[i].HealthScore
			}
			scoreJ := 100
			if items[j].HealthScore != nil {
				scoreJ = *items[j].HealthScore
			}
			return scoreI < scoreJ
		})
	}
}

func TestHealthGradeFilter(t *testing.T) {
	gradeA := "A"
	gradeD := "D"
	gradeF := "F"
	scoreA := 95
	scoreD := 45
	scoreF := 20

	items := []sessionListItem{
		{SessionID: "sess1", HealthGrade: &gradeA, HealthScore: &scoreA},
		{SessionID: "sess2", HealthGrade: &gradeD, HealthScore: &scoreD},
		{SessionID: "sess3", HealthGrade: &gradeF, HealthScore: &scoreF},
		{SessionID: "sess4", HealthGrade: nil, HealthScore: nil},
	}

	// 测试过滤 D,F
	filtered := testFilterByHealthGrade(items, "D,F")
	if len(filtered) != 3 {
		t.Errorf("Expected 3 items (D, F, and nil), got %d", len(filtered))
	}

	// 测试过滤 A
	filtered = testFilterByHealthGrade(items, "A")
	if len(filtered) != 2 {
		t.Errorf("Expected 2 items (A and nil), got %d", len(filtered))
	}

	// 测试空过滤
	filtered = testFilterByHealthGrade(items, "")
	if len(filtered) != 4 {
		t.Errorf("Expected 4 items (no filter), got %d", len(filtered))
	}
}

func TestSessionListSort(t *testing.T) {
	gradeA := "A"
	gradeD := "D"
	gradeF := "F"
	scoreA := 95
	scoreD := 45
	scoreF := 20

	// 测试默认排序（status 优先，然后健康分升序）
	items := []sessionListItem{
		{SessionID: "sess1", Status: "active", HealthGrade: &gradeA, HealthScore: &scoreA, TotalCostUSD: 1.0},
		{SessionID: "sess2", Status: "error", HealthGrade: &gradeD, HealthScore: &scoreD, TotalCostUSD: 5.0},
		{SessionID: "sess3", Status: "active", HealthGrade: &gradeF, HealthScore: &scoreF, TotalCostUSD: 3.0},
	}

	testSortSessionList(items, "")
	if items[0].SessionID != "sess2" {
		t.Errorf("Expected sess2 (error status) first, got %s", items[0].SessionID)
	}
	if items[1].SessionID != "sess3" {
		t.Errorf("Expected sess3 (F grade, score 20) second, got %s", items[1].SessionID)
	}
	if items[2].SessionID != "sess1" {
		t.Errorf("Expected sess1 (A grade, score 95) last, got %s", items[2].SessionID)
	}

	// 测试按成本排序
	items = []sessionListItem{
		{SessionID: "sess1", Status: "active", HealthGrade: &gradeA, HealthScore: &scoreA, TotalCostUSD: 1.0},
		{SessionID: "sess2", Status: "active", HealthGrade: &gradeD, HealthScore: &scoreD, TotalCostUSD: 5.0},
		{SessionID: "sess3", Status: "active", HealthGrade: &gradeF, HealthScore: &scoreF, TotalCostUSD: 3.0},
	}

	testSortSessionList(items, "cost")
	if items[0].SessionID != "sess2" {
		t.Errorf("Expected sess2 (highest cost $5.0) first, got %s with cost $%.2f", items[0].SessionID, items[0].TotalCostUSD)
	}

	// 测试按健康分排序（health 模式下是降序，高分在前）
	items = []sessionListItem{
		{SessionID: "sess1", Status: "active", HealthGrade: &gradeA, HealthScore: &scoreA, TotalCostUSD: 1.0},
		{SessionID: "sess2", Status: "active", HealthGrade: &gradeD, HealthScore: &scoreD, TotalCostUSD: 5.0},
		{SessionID: "sess3", Status: "active", HealthGrade: &gradeF, HealthScore: &scoreF, TotalCostUSD: 3.0},
	}

	testSortSessionList(items, "health")
	if items[0].SessionID != "sess1" {
		t.Errorf("Expected sess1 (highest health score 95) first, got %s", items[0].SessionID)
	}
	if items[2].SessionID != "sess3" {
		t.Errorf("Expected sess3 (lowest health score 20) last, got %s", items[2].SessionID)
	}
}

func TestStatusPriority(t *testing.T) {
	gradeA := "A"
	scoreA := 95

	items := []sessionListItem{
		{SessionID: "sess1", Status: "active", HealthGrade: &gradeA, HealthScore: &scoreA},
		{SessionID: "sess2", Status: "error", HealthGrade: &gradeA, HealthScore: &scoreA},
		{SessionID: "sess3", Status: "waiting", HealthGrade: &gradeA, HealthScore: &scoreA},
		{SessionID: "sess4", Status: "stopped", HealthGrade: &gradeA, HealthScore: &scoreA},
	}

	testSortSessionList(items, "")
	
	if items[0].Status != "error" {
		t.Errorf("Expected error status first, got %s", items[0].Status)
	}
	if items[1].Status != "waiting" {
		t.Errorf("Expected waiting status second, got %s", items[1].Status)
	}
	if items[2].Status != "active" {
		t.Errorf("Expected active status third, got %s", items[2].Status)
	}
	if items[3].Status != "stopped" {
		t.Errorf("Expected stopped status last, got %s", items[3].Status)
	}
}
