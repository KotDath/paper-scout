// Package research coordinates durable runs with process-local cancellation.
// It does not execute model calls; AgentRunner attaches through Start/Finish.
package research

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"paper-scout/internal/store"
)

type Service struct {
	db      *store.Store
	parent  context.Context
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	closed  bool
	execute func(context.Context, store.Run)
	workers sync.WaitGroup
}

func New(parent context.Context, db *store.Store) *Service {
	return &Service{db: db, parent: parent, active: make(map[string]context.CancelFunc)}
}

func (s *Service) Create(ctx context.Context, owner, conversation, requestID, message string) (store.Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.parent.Err() != nil {
		return store.Run{}, false, context.Canceled
	}
	r, created, err := s.db.CreateRun(ctx, owner, conversation, requestID, message)
	if err == nil && created && s.execute != nil {
		s.workers.Add(1)
		execute := s.execute
		go func() { defer s.workers.Done(); execute(s.parent, r) }()
	}
	return r, created, err
}

// SetExecutor attaches the application worker before serving requests. A nil
// executor preserves the manual lifecycle used by lower-level integrations.
func (s *Service) SetExecutor(execute func(context.Context, store.Run)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execute = execute
}

// Start creates a deadline from server lifetime, never from the short-lived
// request context. Call Finish when the worker stops, including on cancellation.
func (s *Service) Start(ctx context.Context, owner, id string) (context.Context, store.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.parent.Err() != nil {
		return nil, store.Run{}, context.Canceled
	}
	r, err := s.db.StartRun(ctx, owner, id)
	if err != nil {
		return nil, store.Run{}, err
	}
	runCtx, cancel := context.WithTimeout(s.parent, 15*time.Minute)
	s.active[id] = cancel
	return runCtx, r, nil
}

// Cancel persists the request before signalling the worker; a subsequent Finish
// cannot turn a committed cancellation into a completed result.
func (s *Service) Cancel(ctx context.Context, owner, id string) (store.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.CancelRun(ctx, owner, id)
	if err == nil && r.CancelRequested {
		if cancel := s.active[id]; cancel != nil {
			cancel()
		}
	}
	return r, err
}

// Use a fresh persistence context when a run's execution context is cancelled.
func (s *Service) Finish(ctx context.Context, owner, id, status, code string, usage json.RawMessage) (store.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.db.FinishRun(ctx, owner, id, status, code, usage)
	if err == nil {
		if cancel := s.active[id]; cancel != nil {
			cancel()
			delete(s.active, id)
		}
	}
	return r, err
}

// Close only signals workers. Unfinished durable state is recovered as
// interrupted at the next startup, with no automatic paid replay.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for id, cancel := range s.active {
		cancel()
		delete(s.active, id)
	}
}

// Wait is called after HTTP shutdown/Close and before closing SQLite.
func (s *Service) Wait() { s.workers.Wait() }
