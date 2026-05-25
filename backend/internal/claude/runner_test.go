package claude

import "testing"

func TestParseLineSystem(t *testing.T) {
	line := []byte(`{"type":"system","subtype":"init","session_id":"sess-123","model":"claude-opus-4-7"}`)
	ev, ok := parseLine(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Kind != KindSystem || ev.ClaudeSessionID != "sess-123" || ev.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected system event: %+v", ev)
	}
}

func TestParseLineAssistant(t *testing.T) {
	line := []byte(`{"type":"assistant","session_id":"s","message":{"model":"claude-opus-4-7","content":[{"type":"text","text":"Hello "},{"type":"text","text":"world"}]}}`)
	ev, ok := parseLine(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Kind != KindAssistant || ev.Text != "Hello world" {
		t.Fatalf("unexpected assistant event: %+v", ev)
	}
	if ev.Model != "claude-opus-4-7" {
		t.Fatalf("expected model captured, got %q", ev.Model)
	}
}

func TestParseLineAssistantNonTextIgnored(t *testing.T) {
	line := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","text":""}]}}`)
	if _, ok := parseLine(line); ok {
		t.Fatal("expected no event for tool-only assistant message")
	}
}

func TestParseLineResult(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s","total_cost_usd":0.0421,"usage":{"input_tokens":1234,"output_tokens":567}}`)
	ev, ok := parseLine(line)
	if !ok {
		t.Fatal("expected event")
	}
	if ev.Kind != KindResult {
		t.Fatalf("expected result kind, got %s", ev.Kind)
	}
	if ev.InputTokens != 1234 || ev.OutputTokens != 567 {
		t.Fatalf("unexpected usage: %+v", ev)
	}
	if ev.CostUSD < 0.0420 || ev.CostUSD > 0.0422 {
		t.Fatalf("unexpected cost: %v", ev.CostUSD)
	}
}

func TestParseLineResultError(t *testing.T) {
	line := []byte(`{"type":"result","subtype":"error","is_error":true,"result":"boom"}`)
	ev, ok := parseLine(line)
	if !ok || ev.Kind != KindError || ev.Err == nil {
		t.Fatalf("expected error event, got %+v ok=%v", ev, ok)
	}
}

func TestParseLineGarbage(t *testing.T) {
	for _, l := range [][]byte{[]byte(""), []byte("   "), []byte("not json"), []byte(`{"type":"user"}`)} {
		if _, ok := parseLine(l); ok {
			t.Fatalf("expected no event for %q", l)
		}
	}
}
