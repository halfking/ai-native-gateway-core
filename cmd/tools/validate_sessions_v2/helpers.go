package main

// Helper functions for validation

// abs returns the absolute value of an int64
func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// abs64 returns the absolute value of a float64
func abs64(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// max64 returns the larger of two float64 values
func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
