package gateway

import (
	"encoding/json"
	"time"
)

// OpCode 是 WebSocket 协议的操作码。
type OpCode int

const (
	// OpDispatch 是服务端事件推送
	OpDispatch OpCode = 0
	// OpHeartbeat 是客户端心跳
	OpHeartbeat OpCode = 1
	// OpIdentify 是首次鉴权
	OpIdentify OpCode = 2
	// OpResume 是断线恢复
	OpResume       OpCode = 6
	OpReconnect    OpCode = 7
	OpInvalidSess  OpCode = 9
	OpHello        OpCode = 10
	OpHeartbeatAck OpCode = 11
)

// 事件类型。
const (
	EventReady              = "READY"
	EventResumed            = "RESUMED"
	EventGroupAtMessage     = "GROUP_AT_MESSAGE_CREATE"
	EventGroupMessage       = "GROUP_MESSAGE_CREATE"
	EventC2CMessage         = "C2C_MESSAGE_CREATE"
	EventAtMessage          = "AT_MESSAGE_CREATE"
	EventDirectMessage      = "DIRECT_MESSAGE_CREATE"
	EventGroupAddBot        = "GROUP_ADD_ROBOT"
	EventGroupDelBot        = "GROUP_DEL_ROBOT"
	EventGroupMsgReject     = "GROUP_MSG_REJECT"
	EventGroupMsgReceive    = "GROUP_MSG_RECEIVE"
	EventC2CFriendAdd       = "FRIEND_ADD"
	EventC2CFriendDel       = "FRIEND_DEL"
	EventInteractionCreate  = "INTERACTION_CREATE"
	EventSubscribeMsgStatus = "SUBSCRIBE_MESSAGE_STATUS"
)

// 订阅意图位。
const (
	// IntentsGroup 订阅群聊 @ 与单聊消息
	IntentsGroup = 1 << 25
	// IntentsInteraction 订阅按钮、单聊快捷菜单等互动事件
	IntentsInteraction = 1 << 26
	// IntentsGuildAt 订阅频道 @ 消息
	IntentsGuildAt = 1 << 30
)

// Interaction 是 INTERACTION_CREATE 事件体。
type Interaction struct {
	ID         string          `json:"id"`
	Type       int             `json:"type"`
	Scene      string          `json:"scene"`
	ChatType   int             `json:"chat_type"`
	UserOpenID string          `json:"user_openid"`
	GroupOpen  string          `json:"group_openid"`
	Data       InteractionData `json:"data"`
}

// InteractionData 是互动数据。
type InteractionData struct {
	Type     int                 `json:"type"`
	Resolved InteractionResolved `json:"resolved"`
}

// InteractionResolved 是解析后的互动数据：按钮的 data/id 就在这里。
type InteractionResolved struct {
	ButtonData string `json:"button_data"`
	ButtonID   string `json:"button_id"`
	FeatureID  string `json:"feature_id"`
	MessageID  string `json:"message_id"`
}

// Payload 是网关下发的原始帧。
type Payload struct {
	Op     OpCode          `json:"op"`
	Seq    int64           `json:"s,omitempty"`
	Type   string          `json:"t,omitempty"`
	ID     string          `json:"id,omitempty"`
	Data   json.RawMessage `json:"d,omitempty"`
	Raw    []byte          `json:"-"`
	Closed bool            `json:"-"`
}

// Event 是交给业务层的事件。
type Event struct {
	Type      string
	Seq       int64
	Data      json.RawMessage
	Received  time.Time
	SessionID string
}

// readyData 是 READY 事件的载荷。
type readyData struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	User      User   `json:"user"`
	Shard     []int  `json:"shard"`
}

// helloData 是 Hello 帧的载荷。
type helloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// MessageAuthor 是消息发送者。
type MessageAuthor struct {
	ID          string `json:"id"`
	UnionOpenID string `json:"union_openid"`
	Username    string `json:"username"`
	UserOpenID  string `json:"user_openid"`
	MemberOpen  string `json:"member_openid"`
	Bot         bool   `json:"bot"`
}

// ArkData 是结构化卡片消息。
type ArkData struct {
	Prompt  string            `json:"prompt"`
	ArkType string            `json:"ark_type"`
	ArkName string            `json:"ark_name"`
	Fields  map[string]string `json:"fields"`
}

// MsgElement 是引用消息里的元素。
type MsgElement struct {
	Content string         `json:"content"`
	Author  *MessageAuthor `json:"author"`
}

// InboundMessage 是事件里的消息体，群聊 / 单聊 / 频道共用。
type InboundMessage struct {
	ID          string         `json:"id"`
	Content     string         `json:"content"`
	MessageType int            `json:"message_type"`
	ArkData     *ArkData       `json:"ark_data"`
	MsgElements []MsgElement   `json:"msg_elements"`
	Timestamp   string         `json:"timestamp"`
	GroupID     string         `json:"group_id"`
	GroupOpen   string         `json:"group_openid"`
	ChannelID   string         `json:"channel_id"`
	GuildID     string         `json:"guild_id"`
	Author      *MessageAuthor `json:"author"`
	Member      *struct {
		Nick      string `json:"nick"`
		JoinedAt  string `json:"joined_at"`
		UnionOpen string `json:"union_openid"`
	} `json:"member"`
	Mentions    []MessageAuthor `json:"mentions"`
	Attachments []Attachment    `json:"attachments"`
}

// Attachment 是消息附件（图片/语音/视频/文件）。
type Attachment struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	Size        int    `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	// 语音消息专用
	VoiceWavURL  string `json:"voice_wav_url"`
	ASRReferText string `json:"asr_refer_text"`
}

// ChatName 返回可读的会话名称（平台通常只给 openid）。
func (m *InboundMessage) ChatName() string {
	if m.Author != nil && m.Author.Username != "" {
		return m.Author.Username
	}
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	return ""
}

// Time 解析消息时间戳。
func (m *InboundMessage) Time() time.Time {
	if m.Timestamp == "" {
		return time.Now()
	}
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
		return t
	}
	return time.Now()
}

// User 是机器人自身资料（READY 事件里下发）。
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
	Avatar   string `json:"avatar"`
}
