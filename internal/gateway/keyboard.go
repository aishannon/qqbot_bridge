package gateway

// Keyboard 是消息底部的内联按钮组件（C2C / 群聊通用）。
//
// 按钮点击会触发 INTERACTION_CREATE 事件（type=11），服务端必须在 3 秒内
// 调用 PutInteraction 回应，否则用户端会一直转圈。
type Keyboard struct {
	Content *KeyboardContent `json:"content,omitempty"`
}

// KeyboardContent 是自定义按钮的按钮矩阵。
type KeyboardContent struct {
	Rows []KeyboardRow `json:"rows"`
}

// KeyboardRow 是一行按钮。
type KeyboardRow struct {
	Buttons []KeyboardButton `json:"buttons"`
}

// KeyboardButton 是单个按钮。
type KeyboardButton struct {
	ID         string         `json:"id"`
	RenderData KeyboardRender `json:"render_data"`
	Action     KeyboardAction `json:"action"`
}

// KeyboardRender 控制按钮外观。
type KeyboardRender struct {
	Label        string `json:"label"`
	VisitedLabel string `json:"visited_label,omitempty"`
	// Style 0=灰色线框 1=蓝色线框 3=白底红字 4=蓝底白字
	Style int `json:"style,omitempty"`
}

// 按钮操作类型。
const (
	// ActionTypeCallback 回调：点击触发 INTERACTION_CREATE，data 原样回传
	ActionTypeCallback = 1
	// ActionTypeCommand 指令：点击把 data 填进输入框（部分场景会需要用户手动发送）
	ActionTypeCommand = 2
	// ActionTypeLink 跳转链接
	ActionTypeLink = 0
)

// KeyboardAction 描述按钮点击后做什么。
type KeyboardAction struct {
	Type int    `json:"type"`
	Data string `json:"data"`
	// Enter 点击后是否直接发送消息
	Enter bool `json:"enter"`
	// Reply 是否把 data 当作一条消息回复给机器人
	Reply bool `json:"reply,omitempty"`
	// ClickLimit 可点击次数，0 表示不限
	ClickLimit int         `json:"click_limit,omitempty"`
	Permission *Permission `json:"permission,omitempty"`
}

// Permission 是按钮的可点击范围。
type Permission struct {
	// Type 0=仅消息接收者 1=管理员 2=所有人
	Type           int      `json:"type"`
	SpecifyUserIDs []string `json:"specify_user_ids,omitempty"`
}

// CallbackButton 构造一个点击后回调服务端的按钮。
func CallbackButton(id, label, data string) KeyboardButton {
	return KeyboardButton{
		ID:         id,
		RenderData: KeyboardRender{Label: label, VisitedLabel: label, Style: 1},
		Action: KeyboardAction{
			Type:       ActionTypeCallback,
			Data:       data,
			Permission: &Permission{Type: 2},
		},
	}
}

// NewKeyboard 把按钮按每行 perRow 个排成键盘。
func NewKeyboard(buttons []KeyboardButton, perRow int) *Keyboard {
	if len(buttons) == 0 {
		return nil
	}
	if perRow <= 0 {
		perRow = 3
	}
	kb := &Keyboard{Content: &KeyboardContent{}}
	for i := 0; i < len(buttons); i += perRow {
		end := i + perRow
		if end > len(buttons) {
			end = len(buttons)
		}
		kb.Content.Rows = append(kb.Content.Rows, KeyboardRow{Buttons: buttons[i:end]})
	}
	return kb
}

// ---------- 单聊底部快捷菜单 ----------

// 菜单项类型。
const (
	MenuTypeSendMessage = "send_message"
	MenuTypeLink        = "link"
	MenuTypeSwitch      = "switch"
	MenuTypeMenu        = "menu"
)

// MenuItem 是单聊输入框下方功能面板里的一项。
type MenuItem struct {
	Name         string     `json:"name"`
	Type         string     `json:"type"`
	SendMessage  string     `json:"send_message,omitempty"`
	Link         string     `json:"link,omitempty"`
	SubMenuItems []MenuItem `json:"sub_menu_items,omitempty"`
}
