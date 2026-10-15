package sanitizerules

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisNotifications struct {
	client  redis.UniversalClient
	channel string
}

func NewRedisNotifications(client redis.UniversalClient, channel string) *RedisNotifications {
	if channel == "" {
		channel = "llmgw:sanitize_rules:changed"
	}
	return &RedisNotifications{client: client, channel: channel}
}
func (n *RedisNotifications) Publish(ctx context.Context, version int64) error {
	if n == nil || n.client == nil {
		return errors.New("sanitize rule notification unavailable")
	}
	if err := n.client.Publish(ctx, n.channel, strconv.FormatInt(version, 10)).Err(); err != nil {
		return errors.New("sanitize rule notification failed")
	}
	return nil
}
func (n *RedisNotifications) Subscribe(ctx context.Context) (<-chan int64, error) {
	if n == nil || n.client == nil {
		return nil, errors.New("sanitize rule notification unavailable")
	}
	handshake, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	pubsub := n.client.Subscribe(handshake, n.channel)
	// Receive ignores the default client's ReadTimeout for pubsub. Set an actual
	// socket deadline so a peer which accepts TCP but withholds ACK cannot hang.
	stopClose := context.AfterFunc(ctx, func() { _ = pubsub.Close() })
	defer stopClose()
	if _, err := pubsub.ReceiveTimeout(handshake, time.Second); err != nil {
		_ = pubsub.Close()
		return nil, errors.New("sanitize rule subscription failed")
	}
	out := make(chan int64, 1)
	go func() {
		defer close(out)
		defer pubsub.Close()
		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				id, err := strconv.ParseInt(message.Payload, 10, 64)
				if err != nil || id < 1 {
					continue
				}
				select {
				case out <- id:
				default:
				}
			}
		}
	}()
	return out, nil
}
