package openrouter

import (
	"encoding/json"
	"testing"
)

func FuzzResponseProtocol(f *testing.F) {
	for _, seed := range []string{
		`null`, `{}`, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`,
		`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"x","type":"function","function":{"name":"test","arguments":"{}"}}]}}]}`,
		`{"error":{"message":"upstream error"}}`, `{"choices":[{"finish_reason":"length"}]}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		m := &Model{key: "fixture-secret"}
		response, err := m.decodeResponse(200, []byte(data))
		if err != nil {
			return
		}
		seen := make(map[string]bool)
		for _, call := range response.ToolCalls {
			var args map[string]json.RawMessage
			if call.ID == "" || seen[call.ID] || json.Unmarshal(call.Arguments, &args) != nil || args == nil {
				t.Fatalf("accepted malformed tool call: %+v", call)
			}
			seen[call.ID] = true
		}
	})
}
