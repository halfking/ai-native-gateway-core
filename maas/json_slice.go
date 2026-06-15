package maas

// jsonSlice ensures empty Go slices encode as JSON [] instead of null.
func jsonSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
