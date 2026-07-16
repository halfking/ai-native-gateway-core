package trace

import "os"

var globalEnv = func() [][2]string {
	pairs := [][2]string{}
	for _, k := range []string{"LLM_GATEWAY_REDIS_ADDR"} {
		if v := os.Getenv(k); v != "" {
			pairs = append(pairs, [2]string{k, v})
		}
	}
	return pairs
}()
