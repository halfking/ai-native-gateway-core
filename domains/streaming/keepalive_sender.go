package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// KeepaliveEvent represents a keepalive event sent to client
type KeepaliveEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
}

// NodeSwitchEvent represents a node switch notification
type NodeSwitchEvent struct {
	Type      string `json:"type"`
	FromNode  string `json:"from_node"`
	ToNode    string `json:"to_node"`
	Attempt   int    `json:"attempt"`
	Reason    string `json:"reason,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// KeepaliveSender manages keepalive heartbeats for streaming requests
type KeepaliveSender struct {
	writer      http.ResponseWriter
	flusher     http.Flusher
	interval    time.Duration
	stopChan    chan struct{}
	stoppedChan chan struct{}
	logger      *slog.Logger
}

// NewKeepaliveSender creates a new KeepaliveSender
func NewKeepaliveSender(w http.ResponseWriter, intervalSeconds int, logger *slog.Logger) *KeepaliveSender {
	flusher, ok := w.(http.Flusher)
	if !ok {
		logger.Warn("response writer does not support flushing, keepalive disabled")
		return nil
	}

	if intervalSeconds <= 0 {
		intervalSeconds = 15 // default
	}

	return &KeepaliveSender{
		writer:      w,
		flusher:     flusher,
		interval:    time.Duration(intervalSeconds) * time.Second,
		stopChan:    make(chan struct{}),
		stoppedChan: make(chan struct{}),
		logger:      logger,
	}
}

// Start begins sending keepalive events
func (k *KeepaliveSender) Start(ctx context.Context) {
	if k == nil {
		return
	}

	go func() {
		defer close(k.stoppedChan)

		ticker := time.NewTicker(k.interval)
		defer ticker.Stop()

		k.logger.Debug("keepalive sender started", "interval", k.interval)

		for {
			select {
			case <-ctx.Done():
				k.logger.Debug("keepalive sender stopped (context done)")
				return
			case <-k.stopChan:
				k.logger.Debug("keepalive sender stopped (explicit stop)")
				return
			case <-ticker.C:
				if err := k.sendKeepalive(); err != nil {
					k.logger.Warn("failed to send keepalive", "error", err)
				}
			}
		}
	}()
}

// Stop stops the keepalive sender
func (k *KeepaliveSender) Stop() {
	if k == nil {
		return
	}

	close(k.stopChan)
	<-k.stoppedChan // wait for goroutine to exit
}

// sendKeepalive sends a keepalive event immediately
func (k *KeepaliveSender) sendKeepalive() error {
	if k == nil {
		return nil
	}

	event := KeepaliveEvent{
		Type:      "keepalive",
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal keepalive event: %w", err)
	}

	// SSE format
	fmt.Fprintf(k.writer, "event: keepalive\n")
	fmt.Fprintf(k.writer, "data: %s\n\n", data)
	k.flusher.Flush()

	k.logger.Debug("keepalive sent", "timestamp", event.Timestamp)
	return nil
}

// SendNodeSwitch sends a node switch notification
func (k *KeepaliveSender) SendNodeSwitch(fromNode, toNode string, attempt int, reason string) error {
	if k == nil {
		return nil
	}

	event := NodeSwitchEvent{
		Type:      "node_switch",
		FromNode:  fromNode,
		ToNode:    toNode,
		Attempt:   attempt,
		Reason:    reason,
		Timestamp: time.Now().Unix(),
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal node switch event: %w", err)
	}

	// SSE format
	fmt.Fprintf(k.writer, "event: node_switch\n")
	fmt.Fprintf(k.writer, "data: %s\n\n", data)
	k.flusher.Flush()

	k.logger.Info("node switch notification sent",
		"from", fromNode,
		"to", toNode,
		"attempt", attempt,
		"reason", reason)
	return nil
}
