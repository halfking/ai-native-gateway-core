package domain

// CostContext 封装成本控制领域的上下文。
type CostContext struct {
	EstimatedUSD  float64
	BudgetLimit   float64
	BudgetOK      bool
	TokenCountIn  int
	TokenCountOut int
	Tags          []string
}

// CostResponseContext 成本响应上下文。
type CostResponseContext struct {
	ActualUSD     float64
	TokenCountIn  int
	TokenCountOut int
	OverBudget    bool
}
