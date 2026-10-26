// Package halt holds the proxy's circuit breaker. A single block-filter match
// is treated as a breach: the proxy stops forwarding entirely and answers 403
// until a human resumes it. The state is process-local, so restarting the
// server clears it.
package halt

import (
	"sync"
	"time"
)

// Status is a snapshot of the breaker, safe to serialize.
type Status struct {
	Halted  bool      `json:"halted"`
	At      time.Time `json:"halted_at,omitzero"`
	Filter  string    `json:"halted_filter,omitempty"`
	Refused int       `json:"refused"`
}

// State is the shared breaker. It is safe for concurrent use.
type State struct {
	mu sync.RWMutex
	st Status
}

// New returns a breaker in the running (not halted) state.
func New() *State { return &State{} }

// Trigger halts the proxy and reports whether this call caused the transition.
// Repeated triggers keep the first breach's time and filter so the cause is
// never overwritten.
func (s *State) Trigger(filter string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.st.Halted
	if first {
		s.st.Halted = true
		s.st.At = time.Now()
		s.st.Filter = filter
	}
	return s.st, first
}

// Refuse counts one rejected request and returns the current status.
func (s *State) Refuse() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Halted {
		s.st.Refused++
	}
	return s.st
}

// Resume clears the breaker and its counters.
func (s *State) Resume() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = Status{}
	return s.st
}

// Status returns the current state.
func (s *State) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.st
}
