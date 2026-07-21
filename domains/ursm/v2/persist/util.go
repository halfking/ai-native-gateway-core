package persist

import "strconv"

func atoi(s string) int           { n, _ := strconv.Atoi(s); return n }
func atoi64(s string) int64       { n, _ := strconv.ParseInt(s, 10, 64); return n }
func parseFloat(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
