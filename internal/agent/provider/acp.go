package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const (
	JSONRPCVersion = "2.0"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

type ContentType string

const (
	ContentTypeText       ContentType = "text"
	ContentTypeToolUse    ContentType = "tool_use"
	ContentTypeToolResult ContentType = "tool_result"
	ContentTypeImage      ContentType = "image"
	ContentTypeThinking   ContentType = "thinking"
)

type ToolType string

const (
	ToolTypeFunction ToolType = "function"
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ContentBlock struct {
	Type ContentType `json:"type"`

	Text string `json:"text,omitempty"`

	// ID is used by assistant tool_use blocks. Tool result blocks refer back to
	// it via ToolUseID.
	ID        string `json:"id,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`

	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Content any             `json:"content,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Source  *ImageSource    `json:"source,omitempty"`

	// Extended-thinking providers attach signed thinking blocks to assistant
	// messages. Preserve these fields when conversation history is round-tripped;
	// dropping them can make later tool-call requests fail validation.
	Thinking         string `json:"thinking,omitempty"`
	Signature        string `json:"signature,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`

	extra map[string]json.RawMessage `json:"-"`
}

func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*b = ContentBlock(decoded)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{
		"type", "text", "id", "tool_use_id", "name", "input", "content",
		"is_error", "source", "thinking", "signature", "reasoning_content",
	} {
		delete(raw, key)
	}
	if len(raw) > 0 {
		b.extra = raw
	}
	return nil
}

func (b ContentBlock) MarshalJSON() ([]byte, error) {
	type alias ContentBlock
	data, err := json.Marshal(alias(b))
	if err != nil {
		return nil, err
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	for key, value := range b.extra {
		if _, exists := merged[key]; !exists {
			merged[key] = value
		}
	}
	return json.Marshal(merged)
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`

	extra      map[string]json.RawMessage `json:"-"`
	contentRaw json.RawMessage            `json:"-"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	*m = Message{}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if role, ok := raw["role"]; ok {
		if err := json.Unmarshal(role, &m.Role); err != nil {
			return err
		}
	}
	if content, ok := raw["content"]; ok {
		trimmed := bytes.TrimSpace(content)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			if err := json.Unmarshal(content, &m.Content); err != nil {
				return err
			}
		} else {
			m.contentRaw = append(m.contentRaw[:0], content...)
		}
	}

	delete(raw, "role")
	delete(raw, "content")
	if len(raw) > 0 {
		m.extra = raw
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	merged := make(map[string]json.RawMessage, len(m.extra)+2)
	for key, value := range m.extra {
		merged[key] = value
	}
	role, err := json.Marshal(m.Role)
	if err != nil {
		return nil, err
	}
	merged["role"] = role

	if m.Content != nil {
		content, err := json.Marshal(m.Content)
		if err != nil {
			return nil, err
		}
		merged["content"] = content
	} else if m.contentRaw != nil {
		merged["content"] = m.contentRaw
	} else {
		merged["content"] = json.RawMessage("null")
	}
	return json.Marshal(merged)
}

type Tool struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema *InputSchema `json:"input_schema,omitempty"`
}

type InputSchema struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Required   []string       `json:"required,omitempty"`
	Additional map[string]any `json:"-"`
}

func (s *InputSchema) MarshalJSON() ([]byte, error) {
	type Alias InputSchema
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	data, err := json.Marshal(aux)
	if err != nil {
		return nil, err
	}
	if len(s.Additional) == 0 {
		return data, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, err
	}
	for k, v := range s.Additional {
		merged[k] = v
	}
	return json.Marshal(merged)
}

func (s *InputSchema) UnmarshalJSON(data []byte) error {
	type Alias InputSchema
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	delete(raw, "type")
	delete(raw, "properties")
	delete(raw, "required")
	if len(raw) > 0 {
		s.Additional = raw
	}
	return nil
}

type InitializeParams struct {
	ProtocolVersion string             `json:"protocol_version"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"client_info"`
	Meta            map[string]any     `json:"_meta,omitempty"`
}

type ClientCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type ResourcesCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string             `json:"protocol_version"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"server_info"`
	Instructions    string             `json:"instructions,omitempty"`
}

type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ListToolsParams struct {
	Cursor string         `json:"cursor,omitempty"`
	Meta   map[string]any `json:"_meta,omitempty"`
}

type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type CallToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

type CallToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type CreateMessageParams struct {
	Messages    []Message      `json:"messages"`
	Model       string         `json:"model,omitempty"`
	MaxTokens   int            `json:"max_tokens,omitempty"`
	Temperature float64        `json:"temperature,omitempty"`
	System      []ContentBlock `json:"system,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

type CreateMessageResult struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
	Model   string         `json:"model,omitempty"`
	Usage   *Usage         `json:"usage,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

type TextContent struct {
	Type ContentType `json:"type"`
	Text string      `json:"text"`
}

func NewTextContent(text string) ContentBlock {
	return ContentBlock{
		Type: ContentTypeText,
		Text: text,
	}
}

type ToolUseContent struct {
	Type  ContentType     `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func NewToolUseContent(id, name string, input any) (ContentBlock, error) {
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return ContentBlock{}, fmt.Errorf("marshal tool input: %w", err)
	}
	return ContentBlock{
		Type:  ContentTypeToolUse,
		ID:    id,
		Name:  name,
		Input: inputBytes,
	}, nil
}

type ToolResultContent struct {
	Type      ContentType `json:"type"`
	ToolUseID string      `json:"tool_use_id"`
	Content   any         `json:"content"`
	IsError   bool        `json:"is_error,omitempty"`
}

func NewToolResultContent(toolUseID string, content any, isError bool) ContentBlock {
	return ContentBlock{
		Type:      ContentTypeToolResult,
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   isError,
	}
}

func NewUserMessage(text string) Message {
	return Message{
		Role:    RoleUser,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

func NewUserMessageWithContent(blocks ...ContentBlock) Message {
	return Message{
		Role:    RoleUser,
		Content: blocks,
	}
}

func NewAssistantMessage(text string) Message {
	return Message{
		Role:    RoleAssistant,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

func NewAssistantMessageWithContent(blocks ...ContentBlock) Message {
	return Message{
		Role:    RoleAssistant,
		Content: blocks,
	}
}

func NewSystemMessage(text string) Message {
	return Message{
		Role:    RoleSystem,
		Content: []ContentBlock{NewTextContent(text)},
	}
}

type SimpleMessage struct {
	ID        string    `json:"id,omitempty"`
	From      string    `json:"from,omitempty"`
	To        string    `json:"to,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
	Timestamp time.Time `json:"timestamp,omitempty"`
	Priority  string    `json:"priority,omitempty"`
	Type      string    `json:"type,omitempty"`
}

func TranslateSimpleMessage(sm SimpleMessage) Message {
	var content []ContentBlock
	if sm.Subject != "" && sm.Body != "" {
		content = []ContentBlock{
			NewTextContent(fmt.Sprintf("**%s**\n\n%s", sm.Subject, sm.Body)),
		}
	} else if sm.Body != "" {
		content = []ContentBlock{NewTextContent(sm.Body)}
	} else {
		content = []ContentBlock{NewTextContent("")}
	}
	return Message{
		Role:    RoleUser,
		Content: content,
	}
}

func TranslateMessageToSimple(msg Message) SimpleMessage {
	var body string
	for _, block := range msg.Content {
		if block.Type == ContentTypeText && block.Text != "" {
			if body != "" {
				body += "\n"
			}
			body += block.Text
		}
	}
	return SimpleMessage{
		Body:      body,
		Timestamp: time.Now(),
	}
}

func ToolFromDefinition(name, description string, schema map[string]any) Tool {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = make(map[string]any)
	}

	return Tool{
		Name:        name,
		Description: description,
		InputSchema: &InputSchema{
			Type:       "object",
			Properties: props,
			Required:   getStringSlice(schema["required"]),
		},
	}
}

func getStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func NewInitializeRequest(id any, clientName, clientVersion string) JSONRPCRequest {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities: ClientCapabilities{
			Tools: &ToolsCapability{ListChanged: true},
		},
		ClientInfo: ClientInfo{
			Name:    clientName,
			Version: clientVersion,
		},
	}
	paramsBytes, _ := json.Marshal(params)
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "initialize",
		Params:  paramsBytes,
	}
}

func NewInitializeResponse(id any, serverName, serverVersion, instructions string) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: ServerCapabilities{
				Tools: &ToolsCapability{ListChanged: false},
			},
			ServerInfo: ServerInfo{
				Name:    serverName,
				Version: serverVersion,
			},
			Instructions: instructions,
		},
	}
}

func NewListToolsRequest(id any) JSONRPCRequest {
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "tools/list",
	}
}

func NewListToolsResponse(id any, tools []Tool) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: ListToolsResult{
			Tools: tools,
		},
	}
}

func NewCallToolRequest(id any, name string, args map[string]any) JSONRPCRequest {
	paramsBytes, _ := json.Marshal(CallToolParams{
		Name:      name,
		Arguments: args,
	})
	return JSONRPCRequest{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Method:  "tools/call",
		Params:  paramsBytes,
	}
}

func NewCallToolResponse(id any, content []ContentBlock, isError bool) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: CallToolResult{
			Content: content,
			IsError: isError,
		},
	}
}

func NewErrorResponse(id any, code int, message string, data any) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

func ParseRequest(data []byte) (*JSONRPCRequest, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC request: %w", err)
	}
	if req.JSONRPC != JSONRPCVersion {
		return nil, fmt.Errorf("invalid JSON-RPC version: %s", req.JSONRPC)
	}
	return &req, nil
}

func ParseResponse(data []byte) (*JSONRPCResponse, error) {
	var resp JSONRPCResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC response: %w", err)
	}
	if resp.JSONRPC != JSONRPCVersion {
		return nil, fmt.Errorf("invalid JSON-RPC version: %s", resp.JSONRPC)
	}
	return &resp, nil
}

func (r *JSONRPCRequest) ParseParams(v any) error {
	if len(r.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Params, v); err != nil {
		return fmt.Errorf("parse params: %w", err)
	}
	return nil
}
