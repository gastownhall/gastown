package web

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/steveyegge/gastown/internal/mail"
)

// NotificationMessage is the JSON payload pushed to WebSocket clients.
type NotificationMessage struct {
	Type      string `json:"type"`      // "nudge", "mail", "escalation"
	From      string `json:"from"`      // sender identity
	To        string `json:"to"`        // recipient address
	Subject   string `json:"subject"`   // subject line
	Body      string `json:"body"`      // full message body
	Priority  string `json:"priority"`  // urgency level
	ThreadID  string `json:"threadId"`  // associated bead/thread ID
	CreatedAt string `json:"createdAt"` // ISO 8601 timestamp
}

// Hub maintains active WebSocket client connections for broadcast.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *NotificationMessage
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// Client is an active WebSocket connection.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan *NotificationMessage
}

var (
	defaultHub     *Hub
	defaultHubOnce sync.Once
	upgrader       = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
)

// GetHub returns the singleton Hub, creating it on first call.
func GetHub() *Hub {
	defaultHubOnce.Do(func() {
		defaultHub = NewHub()
		go defaultHub.Run()
	})
	return defaultHub
}

// StartSpoolDrainer relays notifications from the cross-process spool file
// to WebSocket clients. CLI processes (gt mail send, gt escalate) append to
// the spool; this goroutine — running in the dashboard process — drains it
// and broadcasts. townRoot may be empty (draining disabled).
func StartSpoolDrainer(townRoot string) {
	if townRoot == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			GetHub().DrainSpool(mail.SpoolPath(townRoot))
		}
	}()
}

// DrainSpool reads and truncates the notification spool, broadcasting each
// entry to connected clients. Malformed lines are dropped silently.
func (h *Hub) DrainSpool(spoolPath string) {
	f, err := os.OpenFile(spoolPath, os.O_RDWR, 0o600)
	if err != nil {
		return // no spool or unreadable — nothing to drain
	}
	defer f.Close()

	var msgs []*NotificationMessage
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg NotificationMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		msgs = append(msgs, &msg)
	}
	if len(msgs) == 0 {
		return
	}

	// Truncate the drained portion.
	if err := f.Truncate(0); err != nil {
		return
	}
	if _, err := f.Seek(0, 0); err != nil {
		return
	}

	for _, m := range msgs {
		h.Notify(m)
	}
}

// NewHub creates a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *NotificationMessage, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's event loop.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
		case msg := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// Client send buffer full — drop and unregister
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Notify broadcasts a notification message to all connected clients.
// Non-blocking: if no clients are connected or the broadcast channel is full,
// the message is simply dropped (the persistent bead/mail bead is the durable record).
func (h *Hub) Notify(msg *NotificationMessage) {
	if msg.CreatedAt == "" {
		msg.CreatedAt = time.Now().Format(time.RFC3339)
	}
	select {
	case h.broadcast <- msg:
	default:
		// Broadcast channel full or no clients — silently drop.
		// The escalation bead already persists this information.
	}
}

// ServeWS handles WebSocket upgrade requests for the dashboard.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	client := &Client{
		hub:  h,
		conn: conn,
		send: make(chan *NotificationMessage, 256),
	}

	h.register <- client
	defer func() {
		h.unregister <- client
		_ = conn.Close()
	}()

	go client.writePump()
	client.readPump()
}

// readPump drains incoming messages from the client (keeps connection alive).
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Minute)) // deadline is best-effort; ReadMessage handles errors
	c.conn.SetReadLimit(1024)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

// writePump sends broadcast messages to the WebSocket client.
func (c *Client) writePump() {
	defer func() {
		_ = c.conn.Close()
	}()
	for msg := range c.send {
		data, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

// ClientCount returns the number of connected WebSocket clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
