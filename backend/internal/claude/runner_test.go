package claude

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// one asserts parseLine produced exactly a single event and returns it.
func one(t *testing.T, line string) Event {
	t.Helper()
	evs := parseLine([]byte(line))
	if len(evs) != 1 {
		t.Fatalf("expected exactly one event, got %d: %+v", len(evs), evs)
	}
	return evs[0]
}

func TestParseLineSystem(t *testing.T) {
	ev := one(t, `{"type":"system","subtype":"init","session_id":"sess-123","model":"claude-opus-4-7"}`)
	if ev.Kind != KindSystem || ev.ClaudeSessionID != "sess-123" || ev.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected system event: %+v", ev)
	}
}

func TestParseLineTextDelta(t *testing.T) {
	ev := one(t, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`)
	if ev.Kind != KindText || ev.Text != "Hello" {
		t.Fatalf("unexpected text delta: %+v", ev)
	}
}

func TestParseLineThinkingDelta(t *testing.T) {
	ev := one(t, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Let me consider"}}}`)
	if ev.Kind != KindThinking || ev.Text != "Let me consider" {
		t.Fatalf("unexpected thinking delta: %+v", ev)
	}
}

// The streamed content_block_start for a tool carries no input mid-stream, so we
// no longer surface tool events from it — they come from the finalized assistant
// message instead (see TestParseLineAssistantWithTool).
func TestParseLineToolUseStartIgnored(t *testing.T) {
	evs := parseLine([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{}}}}`))
	if len(evs) != 0 {
		t.Fatalf("expected content_block_start to be ignored, got %+v", evs)
	}
}

func TestParseLineAssistant(t *testing.T) {
	ev := one(t, `{"type":"assistant","session_id":"s","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"}]}}`)
	if ev.Kind != KindAssistant || ev.Text != "Hi there" || ev.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected assistant event: %+v", ev)
	}
}

// A finalized assistant message that interleaves text and tool_use expands into
// ordered events, and tool events carry the complete input (the file path).
func TestParseLineAssistantWithTool(t *testing.T) {
	evs := parseLine([]byte(`{"type":"assistant","session_id":"s","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Editing"},{"type":"tool_use","name":"Edit","input":{"file_path":"/repo/a.go"}},{"type":"tool_use","name":"Read","input":{"file_path":"/repo/b.go"}}]}}`))
	if len(evs) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(evs), evs)
	}
	if evs[0].Kind != KindAssistant || evs[0].Text != "Editing" {
		t.Fatalf("unexpected first event: %+v", evs[0])
	}
	if evs[1].Kind != KindToolUse || evs[1].ToolName != "Edit" {
		t.Fatalf("unexpected second event: %+v", evs[1])
	}
	if evs[2].Kind != KindToolUse || evs[2].ToolName != "Read" {
		t.Fatalf("unexpected third event: %+v", evs[2])
	}
	var m map[string]any
	if err := json.Unmarshal(evs[1].ToolInput, &m); err != nil || m["file_path"] != "/repo/a.go" {
		t.Fatalf("tool input not preserved: %s err=%v", evs[1].ToolInput, err)
	}
}

func TestParseLinePermissionRequest(t *testing.T) {
	ev := one(t, `{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"git status"},"permission_suggestions":["allow","deny"]}}`)
	if ev.Kind != KindPermission {
		t.Fatalf("expected permission event, got %+v", ev)
	}
	if ev.RequestID != "req_1" || ev.ToolName != "Bash" {
		t.Fatalf("unexpected permission fields: %+v", ev)
	}
	if len(ev.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions, got %v", ev.Suggestions)
	}
}

func TestParseLineResult(t *testing.T) {
	ev := one(t, `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s","total_cost_usd":0.0421,"usage":{"input_tokens":1234,"output_tokens":567}}`)
	if ev.Kind != KindResult || ev.InputTokens != 1234 || ev.OutputTokens != 567 {
		t.Fatalf("unexpected result event: %+v", ev)
	}
	if ev.CostUSD < 0.0420 || ev.CostUSD > 0.0422 {
		t.Fatalf("unexpected cost: %v", ev.CostUSD)
	}
}

func TestParseLineResultError(t *testing.T) {
	ev := one(t, `{"type":"result","subtype":"error","is_error":true,"result":"boom"}`)
	if ev.Kind != KindError || ev.Err == nil {
		t.Fatalf("expected error event, got %+v", ev)
	}
}

func TestUserMessageTextOnly(t *testing.T) {
	m := userMessage("hello", nil)
	msg := m["message"].(map[string]any)
	if msg["content"] != "hello" {
		t.Fatalf("expected plain string content, got %#v", msg["content"])
	}
}

func TestUserMessageWithImages(t *testing.T) {
	raw := []byte{0x89, 0x50, 0x4e, 0x47}
	m := userMessage("look at image-1", []Image{
		{Name: "image-1.png", MediaType: "image/png", Data: raw},
	})
	blocks, ok := m["message"].(map[string]any)["content"].([]map[string]any)
	if !ok || len(blocks) != 3 {
		t.Fatalf("expected 3 content blocks, got %#v", m["message"])
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "look at image-1" {
		t.Fatalf("unexpected text block: %#v", blocks[0])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != "image-1.png" {
		t.Fatalf("unexpected label block: %#v", blocks[1])
	}
	src := blocks[2]["source"].(map[string]any)
	if blocks[2]["type"] != "image" || src["media_type"] != "image/png" {
		t.Fatalf("unexpected image block: %#v", blocks[2])
	}
	if src["data"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("image data not base64-encoded: %v", src["data"])
	}
}

func TestParseLineIgnored(t *testing.T) {
	cases := []string{
		``,
		`   `,
		`not json`,
		`{"type":"user"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}}`,
		`{"type":"control_request","request_id":"x","request":{"subtype":"other"}}`,
	}
	for _, c := range cases {
		if evs := parseLine([]byte(c)); len(evs) != 0 {
			t.Fatalf("expected no event for %q, got %+v", c, evs)
		}
	}
}
