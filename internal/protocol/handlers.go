package protocol

import (
	"errors"
	"fmt"

	"github.com/jonbaldie/gastown/internal/mail"
)

// ErrNoHandler is returned when a message is a recognized protocol message
// but no handler is registered for its type. This lets callers distinguish
// between "not a protocol message" (false, nil) and "protocol message but
// misrouted/unhandled" (true, ErrNoHandler).
var ErrNoHandler = errors.New("no handler registered for protocol message type")

// Handler processes a protocol message and returns an error if processing failed.
type Handler func(msg *mail.Message) error

// HandlerRegistry maps message types to their handlers.
type HandlerRegistry struct {
	handlers map[MessageType]Handler
}

// NewHandlerRegistry creates a new handler registry.
func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{
		handlers: make(map[MessageType]Handler),
	}
}

// Register adds a handler for a specific message type.
func (r *HandlerRegistry) Register(msgType MessageType, handler Handler) {
	r.handlers[msgType] = handler
}

// Handle dispatches a message to the appropriate handler.
// Returns an error if no handler is registered for the message type.
func (r *HandlerRegistry) Handle(msg *mail.Message) error {
	msgType := ParseMessageType(msg.Subject)
	if msgType == "" {
		return fmt.Errorf("unknown message type for subject: %s", msg.Subject)
	}

	handler, ok := r.handlers[msgType]
	if !ok {
		return fmt.Errorf("no handler registered for message type: %s", msgType)
	}

	return handler(msg)
}

// CanHandle returns true if a handler is registered for the message's type.
func (r *HandlerRegistry) CanHandle(msg *mail.Message) bool {
	msgType := ParseMessageType(msg.Subject)
	if msgType == "" {
		return false
	}

	_, ok := r.handlers[msgType]
	return ok
}

// WitnessHandler defines the interface for Witness protocol handlers.
// The Witness receives messages from Refinery about merge status.
type WitnessHandler interface {
	// HandleMerged is called when a branch was successfully merged.
	HandleMerged(payload *MergedPayload) error

	// HandleMergeFailed is called when a merge attempt failed.
	HandleMergeFailed(payload *MergeFailedPayload) error

	// HandleReworkRequest is called when a branch needs rebasing.
	HandleReworkRequest(payload *ReworkRequestPayload) error
}

// RefineryHandler defines the interface for Refinery protocol handlers.
// The Refinery receives messages from Witness about ready branches.
type RefineryHandler interface {
	// HandleMergeReady is called when a polecat's work is verified and ready.
	HandleMergeReady(payload *MergeReadyPayload) error
}

// WrapWitnessHandlers creates mail handlers from a WitnessHandler.
func WrapWitnessHandlers(h WitnessHandler) *HandlerRegistry {
	registry := NewHandlerRegistry()

	registry.Register(TypeMerged, func(msg *mail.Message) error {
		payload, err := ParseMergedPayload(msg.Body)
		if err != nil {
			return err
		}
		return h.HandleMerged(payload)
	})

	registry.Register(TypeMergeFailed, func(msg *mail.Message) error {
		payload, err := ParseMergeFailedPayload(msg.Body)
		if err != nil {
			return err
		}
		return h.HandleMergeFailed(payload)
	})

	registry.Register(TypeReworkRequest, func(msg *mail.Message) error {
		payload, err := ParseReworkRequestPayload(msg.Body)
		if err != nil {
			return err
		}
		return h.HandleReworkRequest(payload)
	})

	return registry
}

// WrapRefineryHandlers creates mail handlers from a RefineryHandler.
func WrapRefineryHandlers(h RefineryHandler) *HandlerRegistry {
	registry := NewHandlerRegistry()

	registry.Register(TypeMergeReady, func(msg *mail.Message) error {
		payload, err := ParseMergeReadyPayload(msg.Body)
		if err != nil {
			return err
		}
		return h.HandleMergeReady(payload)
	})

	return registry
}

// ProcessProtocolMessage processes a protocol message using the registry.
// It returns (true, nil) if the message was handled successfully,
// (true, error) if handling failed, (true, ErrNoHandler) if the message is
// a recognized protocol message but no handler is registered, or
// (false, nil) if not a protocol message.
func (r *HandlerRegistry) ProcessProtocolMessage(msg *mail.Message) (bool, error) {
	if !IsProtocolMessage(msg.Subject) {
		return false, nil
	}

	if !r.CanHandle(msg) {
		return true, ErrNoHandler
	}

	err := r.Handle(msg)
	return true, err
}
