package kbchat

import (
	"github.com/keybase/go-keybase-chat-bot/kbchat/types/chat1"
	"github.com/keybase/go-keybase-chat-bot/kbchat/types/stellar1"
)

type PaymentHolder struct {
	Payment stellar1.PaymentDetailsLocal `json:"notification"`
}

type Result struct {
	Convs []chat1.ConvSummary `json:"conversations"`
}

type Inbox struct {
	Result Result `json:"result"`
}

type SendResponse struct {
	Result chat1.SendRes `json:"result"`
}

type TypeHolder struct {
	Type string `json:"type"`
}

type Thread struct {
	Result chat1.Thread `json:"result"`
}

type Advertisement struct {
	Alias          string `json:"alias,omitempty"`
	Advertisements []chat1.AdvertiseCommandsParam
}
