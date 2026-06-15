package maas

import (
	"fmt"
	"strings"
)

// PaymentChannel identifies how an order is paid.
type PaymentChannel string

const (
	PaymentAlipay PaymentChannel = "alipay"
	PaymentWechat PaymentChannel = "wechat"
	PaymentManual PaymentChannel = "manual"
)

// PaymentSettings holds merchant account placeholders from maas_settings.
type PaymentSettings struct {
	AlipayAccount   string
	WechatMchID     string
	StubAlipayQRURL string
	StubWechatQRURL string
}

// PaymentQR is the QR payload returned when creating an order.
type PaymentQR struct {
	Channel   PaymentChannel `json:"payment_channel"`
	QRPayload string         `json:"qr_payload"`
	QRURL     string         `json:"qr_url"`
	StubMode  bool           `json:"stub_mode"`
	Hint      string         `json:"hint"`
}

// PaymentProvider generates payment QR data for an order.
type PaymentProvider interface {
	GenerateQR(orderNo string, amountCents int, channel PaymentChannel) PaymentQR
}

// StubQRProvider returns placeholder QR until real Alipay/WeChat accounts are configured.
type StubQRProvider struct {
	Settings PaymentSettings
}

func (p StubQRProvider) GenerateQR(orderNo string, amountCents int, channel PaymentChannel) PaymentQR {
	stub := p.isStubMode(channel)
	payload := fmt.Sprintf("stub://%s/%s/%d", channel, orderNo, amountCents)
	qrURL := ""
	hint := ""

	switch channel {
	case PaymentAlipay:
		qrURL = p.Settings.StubAlipayQRURL
		if stub {
			hint = fmt.Sprintf("占位模式：请备注订单号 %s 后联系客服完成支付（支付宝账号待接入）", orderNo)
		} else {
			hint = fmt.Sprintf("请向支付宝账号 %s 转账 ¥%.2f，备注订单号 %s",
				p.Settings.AlipayAccount, float64(amountCents)/100, orderNo)
		}
	case PaymentWechat:
		qrURL = p.Settings.StubWechatQRURL
		if stub {
			hint = fmt.Sprintf("占位模式：请备注订单号 %s 后联系客服完成支付（微信商户号待接入）", orderNo)
		} else {
			hint = fmt.Sprintf("请向微信商户 %s 支付 ¥%.2f，备注订单号 %s",
				p.Settings.WechatMchID, float64(amountCents)/100, orderNo)
		}
	default:
		hint = fmt.Sprintf("人工确认订单 %s，金额 ¥%.2f", orderNo, float64(amountCents)/100)
	}

	if qrURL == "" {
		qrURL = "/api/maas/orders/stub-qr.svg?order=" + orderNo
	}

	return PaymentQR{
		Channel:   channel,
		QRPayload: payload,
		QRURL:     qrURL,
		StubMode:  stub,
		Hint:      hint,
	}
}

func (p StubQRProvider) isStubMode(channel PaymentChannel) bool {
	switch channel {
	case PaymentAlipay:
		return strings.TrimSpace(p.Settings.AlipayAccount) == ""
	case PaymentWechat:
		return strings.TrimSpace(p.Settings.WechatMchID) == ""
	default:
		return true
	}
}
