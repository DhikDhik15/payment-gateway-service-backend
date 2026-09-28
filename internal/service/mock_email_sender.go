package service

import (
	"context"
	"sync"
)

// MockEmailSender is an in-process EmailSender for unit tests — the same role
// MockPaymentProvider plays for payment flows. It never opens a socket.
//
// Usage:
//
//	mock := NewMockEmailSender()
//	_ = svc.DoSomething(context.Background(), mock)
//	if got := mock.Messages(); len(got) != 1 { ... }
//
//	mock.ShouldFail = true // make Send return ErrEmailSendFailed
type MockEmailSender struct {
	// ShouldFail makes Send return ErrEmailSendFailed (delivery-failure
	// paths, e.g. best-effort invitation email in Phase 8C.2).
	ShouldFail bool

	// Err, when set, is returned verbatim by Send — failure injection for
	// Phase 8C.3B worker classification tests (retryable transport errors,
	// permanent ErrEmailMessageInvalid, SMTP *textproto.Error replies).
	// Takes precedence over ShouldFail.
	Err error

	mu       sync.Mutex
	messages []EmailMessage
}

// NewMockEmailSender returns a MockEmailSender that accepts every message.
func NewMockEmailSender() *MockEmailSender {
	return &MockEmailSender{}
}

// Send records the message (or fails when Err/ShouldFail is set).
func (m *MockEmailSender) Send(ctx context.Context, msg EmailMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Err != nil {
		return m.Err
	}
	if m.ShouldFail {
		return ErrEmailSendFailed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

// Messages returns a copy of every successfully recorded message.
func (m *MockEmailSender) Messages() []EmailMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]EmailMessage, len(m.messages))
	copy(out, m.messages)
	return out
}

// Len returns the number of successfully recorded messages.
func (m *MockEmailSender) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

// Reset clears all recorded messages.
func (m *MockEmailSender) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = nil
}
