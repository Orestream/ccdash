package claude

import (
	"encoding/json"
	"testing"
)

func TestParseLineSystem(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"system","subtype":"init","session_id":"sess-123","model":"claude-opus-4-7"}`))
	if !ok || ev.Kind != KindSystem || ev.ClaudeSessionID != "sess-123" || ev.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected system event: %+v ok=%v", ev, ok)
	}
}

func TestParseLineTextDelta(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}`))
	if !ok || ev.Kind != KindText || ev.Text != "Hello" {
		t.Fatalf("unexpected text delta: %+v ok=%v", ev, ok)
	}
}

func TestParseLineThinkingDelta(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Let me consider"}}}`))
	if !ok || ev.Kind != KindThinking || ev.Text != "Let me consider" {
		t.Fatalf("unexpected thinking delta: %+v ok=%v", ev, ok)
	}
}

func TestParseLineToolUseStart(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{"command":"ls"}}}}`))
	if !ok || ev.Kind != KindToolUse || ev.ToolName != "Bash" {
		t.Fatalf("unexpected tool_use: %+v ok=%v", ev, ok)
	}
	if string(ev.ToolInput) != `{"command":"ls"}` {
		t.Fatalf("unexpected tool input: %s", ev.ToolInput)
	}
}

func TestParseLineAssistant(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"assistant","session_id":"s","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Hi "},{"type":"text","text":"there"}]}}`))
	if !ok || ev.Kind != KindAssistant || ev.Text != "Hi there" || ev.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected assistant event: %+v ok=%v", ev, ok)
	}
}

func TestParseLinePermissionRequest(t *testing.T) {
	line := []byte(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"git status"},"permission_suggestions":["allow","deny"]}}`)
	ev, ok := parseLine(line)
	if !ok || ev.Kind != KindPermission {
		t.Fatalf("expected permission event, got %+v ok=%v", ev, ok)
	}
	if ev.RequestID != "req_1" || ev.ToolName != "Bash" {
		t.Fatalf("unexpected permission fields: %+v", ev)
	}
	if len(ev.Suggestions) != 2 {
		t.Fatalf("expected 2 suggestions, got %v", ev.Suggestions)
	}
}

func TestParseLineResult(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s","total_cost_usd":0.0421,"usage":{"input_tokens":1234,"output_tokens":567}}`)
	ev, ok := parseLine(line)
	if !ok || ev.Kind != KindResult || ev.InputTokens != 1234 || ev.OutputTokens != 567 {
		t.Fatalf("unexpected result event: %+v ok=%v", ev, ok)
	}
	if ev.CostUSD < 0.0420 || ev.CostUSD > 0.0422 {
		t.Fatalf("unexpected cost: %v", ev.CostUSD)
	}
}

func TestParseLineResultError(t *testing.T) {
	ev, ok := parseLine([]byte(`{"type":"result","subtype":"error","is_error":true,"result":"boom"}`))
	if !ok || ev.Kind != KindError || ev.Err == nil {
		t.Fatalf("expected error event, got %+v ok=%v", ev, ok)
	}
}

func TestParseLineIgnored(t *testing.T) {
	cases := [][]byte{
		[]byte(""),
		[]byte("   "),
		[]byte("not json"),
		[]byte(`{"type":"user"}`),
		[]byte(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}}`),
		[]byte(`{"type":"control_request","request_id":"x","request":{"subtype":"other"}}`),
	}
	for _, c := range cases {
		if _, ok := parseLine(c); ok {
			t.Fatalf("expected no event for %q", c)
		}
	}
}

// Ensure ToolInput round-trips as valid JSON we can re-marshal.
func TestToolInputIsValidJSON(t *testing.T) {
	ev, _ := parseLine([]byte(`{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Edit","input":{"file":"a.go","old":"x"}}}}`))
	var m map[string]any
	if err := json.Unmarshal(ev.ToolInput, &m); err != nil {
		t.Fatalf("tool input not valid JSON: %v", err)
	}
}
