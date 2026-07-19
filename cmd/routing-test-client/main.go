package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kaixuan/llm-gateway-go/internal/routingtest"
)

func main() {
	gateway := flag.String("gateway", "http://localhost:8781", "gateway base URL")
	apiKey := flag.String("api-key", "", "gateway API key; required")
	modelsFlag := flag.String("models", "minimax-m3,gpt-5.6-luna,glm-5.2", "comma-separated models, run sequentially")
	clients := flag.Int("clients", 5, "concurrent clients per model")
	rounds := flag.Int("rounds", 5, "conversation rounds per client")
	interval := flag.Duration("interval", 10*time.Second, "wait after each response")
	requestTimeout := flag.Duration("request-timeout", 90*time.Second, "per-request timeout")
	output := flag.String("output", "", "JSON Lines output path; defaults to stdout")
	pingOnly := flag.Bool("ping-only", false, "send one minimal ping per client and model")
	flag.Parse()

	if strings.TrimSpace(*apiKey) == "" {
		fatalf("-api-key is required; do not store real credentials in this repository")
	}
	if *clients < 1 || *rounds < 1 || *interval < 0 || *requestTimeout <= 0 {
		fatalf("clients and rounds must be positive; interval must be non-negative; request-timeout must be positive")
	}

	writer := os.Stdout
	var outputFile *os.File
	var err error
	if *output != "" {
		outputFile, err = os.Create(*output)
		if err != nil {
			fatalf("create output: %v", err)
		}
		defer outputFile.Close()
		writer = outputFile
	}
	encoder := json.NewEncoder(writer)

	client := routingtest.NewClient(*gateway, *apiKey, *requestTimeout)
	for _, model := range splitModels(*modelsFlag) {
		if err := runModel(context.Background(), client, model, *clients, *rounds, *interval, *pingOnly, encoder); err != nil {
			fatalf("%s: %v", model, err)
		}
	}
}

func runModel(ctx context.Context, client *routingtest.Client, model string, clients, rounds int, interval time.Duration, pingOnly bool, encoder *json.Encoder) error {
	var writeMu sync.Mutex
	var workers sync.WaitGroup
	results := make(chan routingtest.Result, clients*rounds)

	for clientIndex := range clients {
		workers.Add(1)
		go func(clientIndex int) {
			defer workers.Done()
			sessionID := fmt.Sprintf("routing-test-%s-%02d", uuid.NewString(), clientIndex)
			messages := make([]routingtest.Message, 0, rounds*2)
			maxRounds := rounds
			if pingOnly {
				maxRounds = 1
			}
			for round := 1; round <= maxRounds; round++ {
				content := "ping"
				maxTokens := 1
				if !pingOnly {
					content = contextPayload(round)
					maxTokens = 128
				}
				messages = append(messages, routingtest.Message{Role: "user", Content: content})
				result := client.Chat(ctx, routingtest.Request{Model: model, Messages: messages, MaxTokens: maxTokens}, sessionID, round)
				results <- result

				if round < maxRounds {
					timer := time.NewTimer(interval)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}(clientIndex)
	}

	go func() {
		workers.Wait()
		close(results)
	}()

	for result := range results {
		writeMu.Lock()
		err := encoder.Encode(result)
		writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}
	return nil
}

func splitModels(value string) []string {
	var models []string
	for _, model := range strings.Split(value, ",") {
		if model = strings.TrimSpace(model); model != "" {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		fatalf("-models contains no model")
	}
	return models
}

func contextPayload(round int) string {
	targetTokens := 0
	switch round {
	case 1:
		targetTokens = 10_000
	case 2:
		targetTokens = 10_000
	case 3:
		targetTokens = 20_000
	default:
		return fmt.Sprintf("round=%d: continue the existing conversation without adding bulk context.", round)
	}
	const fragment = "Routing test context payload. "
	return fmt.Sprintf("round=%d %s", round, strings.Repeat(fragment, (targetTokens*4)/len(fragment)))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "routing-test-client: "+format+"\n", args...)
	os.Exit(2)
}
