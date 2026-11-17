package cursor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// toolCall 是一次工具调用的归一化结果。
type toolCall struct {
	Name  string          // 工具名，如 Read、Shell
	Label string          // 过程回传用的一行摘要，如 "Shell: go test ./..."
	Args  json.RawMessage // 原始参数
}

// toolNames 把 stream-json 里的 xxxToolCall 键映射成和 transcript 一致的工具名。
var toolNames = map[string]string{
	"readToolCall":   "Read",
	"writeToolCall":  "Write",
	"editToolCall":   "Edit",
	"deleteToolCall": "Delete",
	"shellToolCall":  "Shell",
	"grepToolCall":   "Grep",
	"globToolCall":   "Glob",
	"lsToolCall":     "LS",
}

// argKeys 是生成摘要时依次尝试的参数名，取第一个非空的字符串。
// 模式类参数排在 path 前面：Grep 同时带 pattern 和 path，摘要要的是搜什么。
var argKeys = []string{"command", "globPattern", "pattern", "query", "url", "path", "targetDirectory", "description"}

// pathKeys 里的参数是文件路径，在工作目录下时显示相对路径，手机上更好读。
var pathKeys = map[string]bool{"path": true, "targetDirectory": true}

// describeToolCall 解析 tool_call 对象，生成工具名和一行摘要。
//
// tool_call 形如 {"readToolCall":{"args":{...}},"toolCallId":"..."}：
// 真正的工具是唯一一个以 ToolCall 结尾的键；也可能是通用的
// {"function":{"name":"...","arguments":"<json 字符串>"}}。
func describeToolCall(raw json.RawMessage, dir string) toolCall {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return toolCall{Name: "Tool", Label: "Tool"}
	}

	var name string
	var args json.RawMessage
	for key, val := range obj {
		if base, ok := strings.CutSuffix(key, "ToolCall"); ok && base != "" {
			name = toolNames[key]
			if name == "" {
				name = capitalize(base)
			}
			var body struct {
				Args json.RawMessage `json:"args"`
			}
			_ = json.Unmarshal(val, &body)
			args = body.Args
			break
		}
	}
	if name == "" {
		if fn, ok := obj["function"]; ok {
			var f struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			_ = json.Unmarshal(fn, &f)
			name = strings.TrimSpace(f.Name)
			if json.Valid([]byte(f.Arguments)) {
				args = json.RawMessage(f.Arguments)
			}
		}
	}
	if name == "" {
		name = "Tool"
	}
	return toolCall{Name: name, Label: toolLabel(name, args, dir), Args: args}
}

// toolLabel 按参数生成「工具名: 关键参数」，没有可用参数就只给工具名。
func toolLabel(name string, args json.RawMessage, dir string) string {
	var fields map[string]any
	if json.Unmarshal(args, &fields) != nil {
		return name
	}
	for _, key := range argKeys {
		v, ok := fields[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if pathKeys[key] {
			v = shortPath(v, dir)
		}
		return name + ": " + oneLine(v, 80)
	}
	return name
}

// shortPath 把工作目录下的绝对路径缩成相对路径。
func shortPath(p, dir string) string {
	if dir == "" || !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return rel
}

// capitalize 把首字母大写：mcp → Mcp。
func capitalize(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// oneLine 压缩空白并按字符截断。
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
