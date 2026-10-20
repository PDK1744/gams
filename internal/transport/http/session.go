package httptransport

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/PDK1744/gams/pkg/types"
)

var (
	// ErrSessionClosed indicates that the session has already been terminated.
	ErrSessionClosed = errors.New("session closed")
	// ErrBufferFull indicates that the session's outbound message queue is full.
	ErrBufferFull = errors.New("session outbound buffer full")
)

// Session represents an active MCP client connection over SSE.
type Session struct {
	ID         string
	Actor      *types.APIKey
	Tenant     *types.Tenant
	CreatedAt  time.Time
	lastSeenAt atomic.Int64 // Unix nanoseconds
	outbound   chan []byte
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	mu         sync.Mutex
}

// NewSession creates an initialized Session.
func NewSession(actor *types.APIKey, tenant *types.Tenant) *Session {
	id, err := uuid.NewV7()
	idStr := ""
	if err != nil {
		idStr = uuid.NewString()
	} else {
		idStr = id.String()
	}

	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	s := &Session{
		ID:        idStr,
		Actor:     actor,
		Tenant:    tenant,
		CreatedAt: now,
		outbound:  make(chan []byte, 256),
		ctx:       ctx,
		cancel:    cancel,
	}
	s.lastSeenAt.Store(now.UnixNano())
	return s
}

// Context returns the context tied to the lifetime of the session.
func (s *Session) Context() context.Context {
	return s.ctx
}

// Done returns a channel that is closed when the session is closed.
func (s *Session) Done() <-chan struct{} {
	return s.ctx.Done()
}

// Outbound returns the channel of queued SSE messages to be sent to the client.
func (s *Session) Outbound() <-chan []byte {
	return s.outbound
}

// Touch refreshes the session's last-seen timestamp.
func (s *Session) Touch() {
	s.lastSeenAt.Store(time.Now().UnixNano())
}

// LastSeenAt returns the time of the most recent activity on this session.
func (s *Session) LastSeenAt() time.Time {
	return time.Unix(0, s.lastSeenAt.Load())
}

// Send queues a message for delivery over the SSE stream.
func (s *Session) Send(msg []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrSessionClosed
	}

	select {
	case s.outbound <- msg:
		s.Touch()
		return nil
	default:
		return ErrBufferFull
	}
}

// Close gracefully terminates the session and frees its resources.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.closed {
		s.closed = true
		s.cancel()
		close(s.outbound)
	}
}

// SessionManager tracks and manages active MCP sessions.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
	stopCh   chan struct{}
}

// NewSessionManager creates a SessionManager with the specified idle TTL.
func NewSessionManager(ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	sm := &SessionManager{
		sessions: make(map[string]*Session),
		ttl:      ttl,
		stopCh:   make(chan struct{}),
	}
	go sm.reaperLoop()
	return sm
}

func (sm *SessionManager) reaperLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.CleanupStale()
		}
	}
}

// CleanupStale closes and removes sessions that have exceeded the idle TTL.
func (sm *SessionManager) CleanupStale() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for id, session := range sm.sessions {
		if now.Sub(session.LastSeenAt()) > sm.ttl {
			session.Close()
			delete(sm.sessions, id)
		}
	}
}

// CreateSession allocates a new session, registers it, and returns it.
func (sm *SessionManager) CreateSession(actor *types.APIKey, tenant *types.Tenant) *Session {
	sess := NewSession(actor, tenant)
	sm.mu.Lock()
	sm.sessions[sess.ID] = sess
	sm.mu.Unlock()
	return sess
}

// Get retrieves an existing session by ID if present.
func (sm *SessionManager) Get(id string) (*Session, bool) {
	sm.mu.RLock()
	sess, ok := sm.sessions[id]
	sm.mu.RUnlock()
	return sess, ok
}

// Remove unregisters and closes a session.
func (sm *SessionManager) Remove(id string) {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if ok {
		delete(sm.sessions, id)
	}
	sm.mu.Unlock()

	if ok {
		sess.Close()
	}
}

// Close shuts down all sessions and stops background cleanup.
func (sm *SessionManager) Close() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	select {
	case <-sm.stopCh:
		// already stopped
	default:
		close(sm.stopCh)
	}

	for id, sess := range sm.sessions {
		sess.Close()
		delete(sm.sessions, id)
	}
}
