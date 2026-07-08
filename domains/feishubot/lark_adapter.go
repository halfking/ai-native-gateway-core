package feishubot

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domains/notification"
)

// LarkChannelAdapter 把 notification.LarkBotChannel 适配为 feishubot.LarkChannel 接口。
//
// 字段映射：
//   - feishubot.Message.Title + Content  →  飞书文本消息（"Title\n\nContent"）
//   - feishubot.Card → 飞书交互式卡片
//
// 这层薄包装的意义：
//   - 解耦：feishubot 包不直接依赖 domains/notification 的全部类型
//   - 测试：mock LarkChannel 即可，不必启动 HTTP 客户端
//   - 演进：未来若切换到企业应用（app 模式）只需换 adapter
type LarkChannelAdapter struct {
	ch *notification.LarkBotChannel
}

// NewLarkChannelAdapter 构造适配器。
func NewLarkChannelAdapter(ch *notification.LarkBotChannel) *LarkChannelAdapter {
	return &LarkChannelAdapter{ch: ch}
}

// Name 返回渠道标识。
func (a *LarkChannelAdapter) Name() string {
	if a.ch == nil {
		return "lark"
	}
	return a.ch.Name()
}

// Send 发送文本消息。
func (a *LarkChannelAdapter) Send(ctx context.Context, msg *Message) error {
	if a.ch == nil || msg == nil {
		return nil
	}
	nmsg := &notification.Message{
		ID:         msg.ID,
		Title:      msg.Title,
		Content:    msg.Content,
		Recipients: msg.Recipients,
		Priority:   notification.Priority(msg.Priority),
	}
	return a.ch.Send(ctx, nmsg)
}

// SendCard 发送卡片。
func (a *LarkChannelAdapter) SendCard(ctx context.Context, card *Card) error {
	if a.ch == nil || card == nil {
		return nil
	}
	ic := convertCard(card)
	return a.ch.SendCard(ctx, ic)
}

// convertCard 把 feishubot.Card 转成 notification.InteractiveCard。
func convertCard(card *Card) *notification.InteractiveCard {
	if card == nil {
		return nil
	}
	ic := &notification.InteractiveCard{
		Header: notification.CardHeader{
			Title:    card.Header.Title,
			Template: card.Header.Template,
		},
		Metadata: card.Metadata,
	}
	for _, e := range card.Elements {
		ne := notification.CardElement{Type: notification.CardElementType(e.Type), Text: e.Text}
		for _, f := range e.Fields {
			ne.Fields = append(ne.Fields, notification.CardField{
				Key: f.Key, Value: f.Value, Short: f.Short,
			})
		}
		ic.Elements = append(ic.Elements, ne)
	}
	for _, a := range card.Actions {
		ic.Actions = append(ic.Actions, notification.CardAction{
			ID: a.ID, Text: a.Text, Style: a.Style, Value: a.Value,
		})
	}
	return ic
}

// Compile-time assertion.
var _ LarkChannel = (*LarkChannelAdapter)(nil)
