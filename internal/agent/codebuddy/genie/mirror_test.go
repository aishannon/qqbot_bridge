package genie

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prepareStore(t *testing.T, home, cwd string) string {
	t.Helper()
	sum := md5.Sum([]byte(filepath.Clean(cwd)))
	root := filepath.Join(home, ".local", "share", "CodeBuddyExtension", "Data",
		"uid1", "CodeBuddyIDE", "uid1", "history", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "index.json"), `{"conversations":[],"current":""}`)
	writeFile(t, filepath.Join(root, "recent.json"), `{}`)
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func syntheticTranscript(t *testing.T) string {
	t.Helper()
	lines := []string{
		`{"id":"m1","timestamp":1000,"type":"message","role":"user","content":[{"type":"input_text","text":"你好"}],"providerData":{"startsNewUserRequest":true,"conversationRequestId":"req1"}}`,
		`{"id":"r1","timestamp":1100,"type":"reasoning","rawContent":[{"type":"reasoning_text","text":"想一想"}]}`,
		`{"id":"c1","timestamp":1200,"type":"function_call","callId":"call1","name":"Bash","arguments":"{\"command\":\"echo hi\"}","providerData":{"conversationRequestId":"req1"}}`,
		`{"id":"x1","timestamp":1300,"type":"function_call_result","callId":"call1","name":"Bash","status":"completed","output":{"type":"text","text":"hi"}}`,
		`{"id":"m2","timestamp":1400,"type":"message","role":"assistant","content":[{"type":"output_text","text":"完成"}],"providerData":{"conversationRequestId":"req1"}}`,
		`{"id":"t1","timestamp":1500,"type":"ai-title","aiTitle":"测试标题"}`,
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

func TestMirrorWritesGenieStore(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "workspace", "demo")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	root := prepareStore(t, home, cwd)
	transcript := syntheticTranscript(t)

	res, err := mirrorAt(home, cwd, "sess-1", transcript)
	if err != nil {
		t.Fatal(err)
	}
	if res.Messages != 5 || res.Name != "测试标题" {
		t.Fatalf("镜像结果不对：%+v", res)
	}

	var index struct {
		Conversations []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"conversations"`
	}
	data, _ := os.ReadFile(filepath.Join(root, "index.json"))
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Conversations) != 1 || index.Conversations[0].ID != res.ConversationID {
		t.Fatalf("工作区索引未写入：%+v", index.Conversations)
	}

	var conv struct {
		Requests []struct {
			ID       string   `json:"id"`
			Messages []string `json:"messages"`
		} `json:"requests"`
		Messages []struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"messages"`
	}
	data, _ = os.ReadFile(filepath.Join(root, res.ConversationID, "index.json"))
	if err := json.Unmarshal(data, &conv); err != nil {
		t.Fatal(err)
	}
	if len(conv.Requests) != 1 || len(conv.Requests[0].Messages) != 5 {
		t.Fatalf("请求结构不对：%+v", conv.Requests)
	}
	if len(conv.Messages) != 5 {
		t.Fatalf("消息元数据不对：%d", len(conv.Messages))
	}

	// 工具调用消息应带 toolStatus，工具结果消息角色为 tool
	foundStatus, foundTool := false, false
	for _, m := range conv.Messages {
		var raw map[string]any
		data, _ := os.ReadFile(filepath.Join(root, res.ConversationID, "messages", m.ID+".json"))
		_ = json.Unmarshal(data, &raw)
		extra, _ := raw["extra"].(string)
		if len(extra) > 0 && strings.Contains(extra, `"toolStatus"`) {
			foundStatus = true
		}
		if m.Role == "tool" {
			foundTool = true
		}
	}
	if !foundStatus || !foundTool {
		t.Fatalf("工具调用/结果没有完整写入：status=%v tool=%v", foundStatus, foundTool)
	}

	// 重复同步不应该产生重复会话或消息
	res2, err := mirrorAt(home, cwd, "sess-1", transcript)
	if err != nil {
		t.Fatal(err)
	}
	if res2.ConversationID != res.ConversationID {
		t.Fatal("重复同步的会话 ID 不一致")
	}
	data, _ = os.ReadFile(filepath.Join(root, "index.json"))
	_ = json.Unmarshal(data, &index)
	if len(index.Conversations) != 1 {
		t.Fatalf("重复同步产生了重复会话：%d", len(index.Conversations))
	}
	data, _ = os.ReadFile(filepath.Join(root, res.ConversationID, "index.json"))
	_ = json.Unmarshal(data, &conv)
	if len(conv.Messages) != 5 {
		t.Fatalf("重复同步消息数不对：%d", len(conv.Messages))
	}
}

func TestMirrorWithoutIDEStore(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "workspace", "demo")
	res, err := mirrorAt(home, cwd, "sess-1", syntheticTranscript(t))
	if err != nil {
		t.Fatal(err)
	}
	if res.ConversationID != "" {
		t.Fatalf("没有 IDE 历史库时应返回空结果：%+v", res)
	}
}
