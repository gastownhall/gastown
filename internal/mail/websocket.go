package mail

// WebsocketNotifier is an optional callback that the web dashboard registers
// to receive nudge notifications via WebSocket when tmux is not available
// (e.g., in containerized/Kubernetes environments).
//
// When non-nil, notifyRecipient() will invoke this callback in the no-tmux
// fallback path, pushing the message to all connected WebSocket clients.
// This keeps the mail package decoupled from the web package to avoid
// circular imports.
var WebsocketNotifier func(msg *Message)
