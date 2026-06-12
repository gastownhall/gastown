package provider

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNewTextContent(t *testing.T) {
	content := NewTextContent("hello world")
	if content.Type != ContentTypeText {
		t.Errorf("expected type %s, got %s", ContentTypeText, content.Type)
	}
	if content.Text != "hello world" {
		t.Errorf("expected text 'hello world', got %s", content.Text)
	}
}

func TestNewUserMessage(t *testing.T) {
	msg := NewUserMessage("test message")
	if msg.Role != RoleUser {
		t.Errorf("expected role %s, got %s", RoleUser, msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "test message" {
		t.Errorf("expected text 'test message', got %s", msg.Content[0].Text)
	}
}

func TestNewAssistantMessage(t *testing.T) {
	msg := NewAssistantMessage("response")
	if msg.Role != RoleAssistant {
		t.Errorf("expected role %s, got %s", RoleAssistant, msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	if msg.Content[0].Text != "response" {
		t.Errorf("expected text 'response', got %s", msg.Content[0].Text)
	}
}

func TestNewToolUseContent(t *testing.T) {
	input := map[string]any{"path": "/tmp/test"}
	content, err := NewToolUseContent("tool-123", "read_file", input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content.Type != ContentTypeToolUse {
		t.Errorf("expected type %s, got %s", ContentTypeToolUse, content.Type)
	}
	if content.Name != "read_file" {
		t.Errorf("expected name 'read_file', got %s", content.Name)
	}
	var parsed map[string]any
	if err := json.Unmarshal(content.Input, &parsed); err != nil {
		t.Fatalf("failed to parse input: %v", err)
	}
	if parsed["path"] != "/tmp/test" {
		t.Errorf("expected path '/tmp/test', got %v", parsed["path"])
	}
}

func TestNewToolResultContent(t *testing.T) {
	content := NewToolResultContent("tool-123", "file contents", false)
	if content.Type != ContentTypeToolResult {
		t.Errorf("expected type %s, got %s", ContentTypeToolResult, content.Type)
	}
	if content.ToolUseID != "tool-123" {
		t.Errorf("expected tool_use_id 'tool-123', got %s", content.ToolUseID)
	}
	if content.Content != "file contents" {
		t.Errorf("expected content 'file contents', got %v", content.Content)
	}
	if content.IsError {
		t.Error("expected IsError to be false")
	}
}

func TestNewToolResultContent_Error(t *testing.T) {
	content := NewToolResultContent("tool-456", "file not found", true)
	if !content.IsError {
		t.Error("expected IsError to be true")
	}
}

func TestJSONRPCRequest_Marshal(t *testing.T) {
	req := NewInitializeRequest(1, "test-client", "1.0.0")
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", parsed["jsonrpc"])
	}
	if parsed["method"] != "initialize" {
		t.Errorf("expected method 'initialize', got %v", parsed["method"])
	}
}

func TestJSONRPCResponse_Marshal(t *testing.T) {
	resp := NewInitializeResponse(1, "test-server", "1.0.0", "Welcome")
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("expected jsonrpc 2.0, got %v", parsed["jsonrpc"])
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result to be a map")
	}
	if result["protocol_version"] != "2024-11-05" {
		t.Errorf("unexpected protocol_version: %v", result["protocol_version"])
	}
}

func TestParseRequest(t *testing.T) {
	data := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol_version":"2024-11-05"}}`)
	req, err := ParseRequest(data)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if req.Method != "initialize" {
		t.Errorf("expected method 'initialize', got %s", req.Method)
	}
	if req.JSONRPC != JSONRPCVersion {
		t.Errorf("expected jsonrpc %s, got %s", JSONRPCVersion, req.JSONRPC)
	}
}

func TestParseRequest_InvalidVersion(t *testing.T) {
	data := []byte(`{"jsonrpc":"1.0","id":1,"method":"test"}`)
	_, err := ParseRequest(data)
	if err == nil {
		t.Error("expected error for invalid jsonrpc version")
	}
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse(1, MethodNotFound, "method not found", nil)
	if resp.Error == nil {
		t.Fatal("expected error to be set")
	}
	if resp.Error.Code != MethodNotFound {
		t.Errorf("expected code %d, got %d", MethodNotFound, resp.Error.Code)
	}
	if resp.Error.Message != "method not found" {
		t.Errorf("expected message 'method not found', got %s", resp.Error.Message)
	}
}

func TestLocalProvider_Initialize(t *testing.T) {
	provider := NewLocalProvider(ACPProviderConfig{
		Name:         "test-provider",
		Version:      "1.0.0",
		Instructions: "Test instructions",
	})
	ctx := context.Background()
	result, err := provider.Initialize(ctx, "test-client", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("unexpected protocol version: %s", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "test-provider" {
		t.Errorf("expected server name 'test-provider', got %s", result.ServerInfo.Name)
	}
	if result.Instructions != "Test instructions" {
		t.Errorf("expected instructions 'Test instructions', got %s", result.Instructions)
	}
}

func TestLocalProvider_ListTools(t *testing.T) {
	tools := []Tool{
		{Name: "read_file", Description: "Read a file"},
		{Name: "write_file", Description: "Write a file"},
	}
	provider := NewLocalProvider(ACPProviderConfig{
		Name:  "test-provider",
		Tools: tools,
	})
	ctx := context.Background()
	result, err := provider.ListTools(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 tools, got %d", len(result))
	}
	if result[0].Name != "read_file" {
		t.Errorf("expected first tool 'read_file', got %s", result[0].Name)
	}
}

func TestLocalProvider_CallTool_NoCallback(t *testing.T) {
	provider := NewLocalProvider(ACPProviderConfig{
		Name: "test-provider",
	})
	ctx := context.Background()
	result, err := provider.CallTool(ctx, "test_tool", map[string]any{"arg": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected result to be an error when no callback registered")
	}
}

func TestLocalProvider_CallTool_WithCallback(t *testing.T) {
	provider := NewLocalProvider(ACPProviderConfig{
		Name: "test-provider",
	})
	provider.OnToolCall(func(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
		return CallToolResult{
			Content: []ContentBlock{NewTextContent("tool executed: " + name)},
		}, nil
	})
	ctx := context.Background()
	result, err := provider.CallTool(ctx, "test_tool", map[string]any{"arg": "value"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected result not to be an error")
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	if result.Content[0].Text != "tool executed: test_tool" {
		t.Errorf("unexpected content: %s", result.Content[0].Text)
	}
}

func TestLocalProvider_OnSessionStart(t *testing.T) {
	called := false
	var receivedInfo ServerInfo
	provider := NewLocalProvider(ACPProviderConfig{
		Name:    "test-provider",
		Version: "1.0.0",
	})
	provider.OnSessionStart(func(ctx context.Context, info ServerInfo) error {
		called = true
		receivedInfo = info
		return nil
	})
	ctx := context.Background()
	_, err := provider.Initialize(ctx, "test-client", "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected session start callback to be called")
	}
	if receivedInfo.Name != "test-provider" {
		t.Errorf("expected server name 'test-provider', got %s", receivedInfo.Name)
	}
}

func TestLocalProvider_GetStatus(t *testing.T) {
	provider := NewLocalProvider(ACPProviderConfig{
		Name:    "test-provider",
		Version: "1.0.0",
	})
	status := provider.GetStatus()
	if status.State != StateDisconnected {
		t.Errorf("expected state %s, got %s", StateDisconnected, status.State)
	}
	ctx := context.Background()
	_, _ = provider.Initialize(ctx, "test-client", "1.0.0")
	status = provider.GetStatus()
	if status.State != StateReady {
		t.Errorf("expected state %s, got %s", StateReady, status.State)
	}
}

func TestLocalProvider_AddRemoveTool(t *testing.T) {
	provider := NewLocalProvider(ACPProviderConfig{
		Name: "test-provider",
	})
	provider.AddTool(Tool{Name: "new_tool", Description: "A new tool"})
	ctx := context.Background()
	tools, _ := provider.ListTools(ctx)
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	provider.RemoveTool("new_tool")
	tools, _ = provider.ListTools(ctx)
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestTranslateGastownMessage(t *testing.T) {
	msg := TranslateGastownMessage("sender", "recipient", "Test Subject", "Test Body")
	if msg.Role != RoleUser {
		t.Errorf("expected role %s, got %s", RoleUser, msg.Role)
	}
	text := ExtractTextContent(msg)
	if text == "" {
		t.Error("expected non-empty text content")
	}
}

func TestExtractToolCalls(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"path": "/test"})
	msg := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			NewTextContent("Let me read that file"),
			{
				Type:  ContentTypeToolUse,
				Name:  "read_file",
				Input: input,
			},
		},
	}
	calls := ExtractToolCalls(msg)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Errorf("expected tool name 'read_file', got %s", calls[0].Name)
	}
}

func TestExtractToolResults(t *testing.T) {
	msg := Message{
		Role: RoleUser,
		Content: []ContentBlock{
			{
				Type:      ContentTypeToolResult,
				ToolUseID: "tool-123",
				Content:   "file contents",
				IsError:   false,
			},
		},
	}
	results := ExtractToolResults(msg)
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(results))
	}
	if results[0].ToolUseID != "tool-123" {
		t.Errorf("expected tool_use_id 'tool-123', got %s", results[0].ToolUseID)
	}
}

func TestMessagesToFromJSON(t *testing.T) {
	msgs := []Message{
		NewUserMessage("Hello"),
		NewAssistantMessage("Hi there"),
	}
	data, err := MessagesToJSON(msgs)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	parsed, err := MessagesFromJSON(data)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(parsed))
	}
	if parsed[0].Role != RoleUser {
		t.Errorf("expected first message role %s, got %s", RoleUser, parsed[0].Role)
	}
	if parsed[1].Role != RoleAssistant {
		t.Errorf("expected second message role %s, got %s", RoleAssistant, parsed[1].Role)
	}
}

func TestMessagesRoundTripPreservesAssistantReasoningContentWithToolCalls(t *testing.T) {
	input := []byte(`[{"role":"assistant","content":null,"reasoning_content":"kept reasoning","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]`)
	parsed, err := MessagesFromJSON(input)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	data, err := MessagesToJSON(parsed)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var roundTripped []map[string]any
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("failed to parse round trip: %v", err)
	}
	msg := roundTripped[0]
	if msg["reasoning_content"] != "kept reasoning" {
		t.Fatalf("reasoning_content = %v, want preserved value", msg["reasoning_content"])
	}
	if _, ok := msg["tool_calls"].([]any); !ok {
		t.Fatalf("tool_calls not preserved: %v", msg["tool_calls"])
	}
	if msg["content"] != nil {
		t.Fatalf("content = %v, want null preserved", msg["content"])
	}
}

func TestMessagesRoundTripPreservesThinkingBlocks(t *testing.T) {
	input := []byte(`[{"role":"assistant","content":[{"type":"thinking","thinking":"private chain","signature":"sig_123"},{"type":"tool_use","id":"tool_1","name":"read","input":{"path":"README.md"}}]}]`)
	parsed, err := MessagesFromJSON(input)
	if err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed[0].Content[0].Thinking != "private chain" {
		t.Fatalf("thinking field not decoded")
	}
	if parsed[0].Content[1].ID != "tool_1" {
		t.Fatalf("tool_use id not decoded")
	}

	data, err := MessagesToJSON(parsed)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var roundTripped []map[string]any
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("failed to parse round trip: %v", err)
	}
	content := roundTripped[0]["content"].([]any)
	thinking := content[0].(map[string]any)
	if thinking["thinking"] != "private chain" || thinking["signature"] != "sig_123" {
		t.Fatalf("thinking block not preserved: %v", thinking)
	}
	toolUse := content[1].(map[string]any)
	if toolUse["id"] != "tool_1" {
		t.Fatalf("tool_use id not preserved: %v", toolUse)
	}
}

func TestInputSchema_MarshalJSON(t *testing.T) {
	schema := &InputSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path",
			},
		},
		Required: []string{"path"},
		Additional: map[string]any{
			"additionalProperties": false,
		},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed["type"] != "object" {
		t.Errorf("expected type 'object', got %v", parsed["type"])
	}
	if parsed["additionalProperties"] != false {
		t.Errorf("expected additionalProperties false, got %v", parsed["additionalProperties"])
	}
}

func TestIsNotification(t *testing.T) {
	notification := NewInitializedNotification()
	if !IsNotification(&notification) {
		t.Error("expected initialized to be a notification")
	}
	req := NewInitializeRequest(1, "client", "1.0.0")
	if IsNotification(&req) {
		t.Error("expected initialize request not to be a notification")
	}
}
