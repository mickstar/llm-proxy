// Package traffic implements the in-memory, capacity-bounded record of proxied
// exchanges plus a non-blocking fan-out to live subscribers (design D5).
//
// A Log never stores values that a filter matched: entries carry only the
// filtered request body (the caller supplies it) and a summary of what the
// filter engine did (names, actions, counts).
package traffic

import (
	"sync"
	"time"
)

const (
	// MaxRequestBody bounds the request bytes retained per entry.
	MaxRequestBody = 1 << 20 // 1 MiB
	// MaxResponseBody bounds the response bytes retained per entry.
	MaxResponseBody = 256 << 10 // 256 KiB

	// defaultCapacity is used when New receives a non-positive capacity.
	defaultCapacity = 500
	// subBuffer is the per-subscriber delivery buffer. A subscriber that falls
	// further behind than this drops its oldest pending entry.
	subBuffer = 64
)

// Entry is one recorded proxied exchange.
type Entry struct {
	ID                 uint64    `json:"id"`
	Time               time.Time `json:"time"`
	Method             string    `json:"method"`
	Path               string    `json:"path"`
	Model              string    `json:"model"`
	Status             int       `json:"status"`
	DurationMS         int64     `json:"duration_ms"`
	RequestSize        int       `json:"request_size"`
	ResponseSize       int       `json:"response_size"`
	PromptTokens       int       `json:"prompt_tokens,omitempty"`
	CachedPromptTokens int       `json:"cached_prompt_tokens,omitempty"`
	CompletionTokens   int       `json:"completion_tokens,omitempty"`
	TotalTokens        int       `json:"total_tokens,omitempty"`
	CostUSD            float64   `json:"cost_usd,omitempty"`
	FilterSummary      string    `json:"filter_summary,omitempty"`
	RedactionSummary   string    `json:"redaction_summary,omitempty"`
	NotFiltered        bool      `json:"not_content_filtered,omitempty"`
	Blocked            bool      `json:"blocked,omitempty"`
	RequestBody        string    `json:"request_body,omitempty"`
	RequestTruncated   bool      `json:"request_truncated,omitempty"`
	ResponseBody       string    `json:"response_body,omitempty"`
	ResponseTruncated  bool      `json:"response_truncated,omitempty"`
}

// subscriber is one live delivery endpoint registered with a Log.
type subscriber struct {
	ch   chan *Entry
	done chan struct{}
}

// Log is a bounded ring buffer of entries with live subscriber fan-out.
// All methods are safe for concurrent use.
type Log struct {
	mu       sync.Mutex
	buf      []*Entry // ring storage, fixed length == capacity
	start    int      // index of the oldest entry
	count    int      // number of stored entries; 0 <= count <= capacity
	capacity int
	nextID   uint64
	subs     map[uint64]*subscriber
	subID    uint64
}

// New creates a Log retaining at most capacity entries. A non-positive
// capacity selects the default of 500.
func New(capacity int) *Log {
	if capacity < 1 {
		capacity = defaultCapacity
	}
	return &Log{
		buf:      make([]*Entry, capacity),
		capacity: capacity,
		subs:     make(map[uint64]*subscriber),
	}
}

// Record stores a copy of e, assigning a monotonic ID (starting at 1) and a
// Time when zero, then broadcasts it to subscribers without blocking. It
// returns a copy of the stored entry with ID and Time populated; mutating the
// returned entry never affects the log.
func (l *Log) Record(e *Entry) *Entry {
	stored := new(Entry)
	if e != nil {
		*stored = *e
	}

	l.mu.Lock()
	l.nextID++
	stored.ID = l.nextID
	if stored.Time.IsZero() {
		stored.Time = time.Now()
	}

	if l.count < l.capacity {
		l.buf[(l.start+l.count)%l.capacity] = stored
		l.count++
	} else {
		l.buf[l.start] = stored
		l.start = (l.start + 1) % l.capacity
	}

	// Broadcast an independent copy: neither the caller's returned entry nor a
	// subscriber can reach the stored one.
	event := new(Entry)
	*event = *stored
	l.publishLocked(event)
	l.mu.Unlock()

	out := *stored
	return &out
}

// List returns up to limit entries newest-first. limit <= 0 returns all.
func (l *Log) List(limit int) []*Entry {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := l.count
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]*Entry, 0, n)
	for i := range n {
		idx := (l.start + l.count - 1 - i + l.capacity) % l.capacity
		out = append(out, cloneEntry(l.buf[idx]))
	}
	return out
}

// Get returns a copy of the entry with id.
func (l *Log) Get(id uint64) (*Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i := range l.count {
		idx := (l.start + i) % l.capacity
		if l.buf[idx].ID == id {
			return cloneEntry(l.buf[idx]), true
		}
	}
	return nil, false
}

// Len returns the number of stored entries.
func (l *Log) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}

// Subscribe returns a receive-only channel receiving new entries and an
// unsubscribe function. The channel is buffered and never closed; after
// unsubscribe no further entries are delivered. The unsubscribe function is
// idempotent and safe to call concurrently with Record.
func (l *Log) Subscribe() (<-chan *Entry, func()) {
	l.mu.Lock()
	l.subID++
	id := l.subID
	s := &subscriber{
		ch:   make(chan *Entry, subBuffer),
		done: make(chan struct{}),
	}
	l.subs[id] = s
	l.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			// Removal and publication share l.mu, so once delete returns no
			// concurrent publisher can hold a reference to s. Only then is it
			// safe to signal done; the delivery channel is never closed.
			l.mu.Lock()
			delete(l.subs, id)
			l.mu.Unlock()
			close(s.done)
		})
	}
	return s.ch, unsub
}

// publishLocked delivers e to every subscriber without blocking. A full
// subscriber buffer drops its oldest pending entry and retries once, so the
// newest entries always win and the proxy path is never stalled.
// Callers must hold l.mu.
func (l *Log) publishLocked(e *Entry) {
	for _, s := range l.subs {
		if trySend(s.ch, e) {
			continue
		}
		select {
		case <-s.ch:
		default:
		}
		trySend(s.ch, e)
	}
}

// trySend performs a single non-blocking send.
func trySend(ch chan *Entry, e *Entry) bool {
	select {
	case ch <- e:
		return true
	default:
		return false
	}
}

// cloneEntry returns a shallow copy of e. Strings and time.Time are immutable
// values, so a shallow copy fully isolates the result from the source.
func cloneEntry(e *Entry) *Entry {
	if e == nil {
		return nil
	}
	c := new(Entry)
	*c = *e
	return c
}

// BodyCapture tee-captures up to limit bytes written to it and flags
// truncation. It reports the full byte count even after the cap is reached.
// It is not safe for concurrent use.
type BodyCapture struct {
	limit     int
	buf       []byte
	size      int
	truncated bool
}

// NewBodyCapture returns a capture retaining at most limit bytes. A negative
// limit is treated as zero.
func NewBodyCapture(limit int) *BodyCapture {
	if limit < 0 {
		limit = 0
	}
	return &BodyCapture{limit: limit}
}

// Write captures p (up to the limit) and always reports len(p), nil so it can
// tee a stream without disturbing it.
func (c *BodyCapture) Write(p []byte) (int, error) {
	c.size += len(p)
	if room := c.limit - len(c.buf); room > 0 {
		n := len(p)
		if n > room {
			n = room
		}
		c.buf = append(c.buf, p[:n]...)
	}
	if c.size > c.limit {
		c.truncated = true
	}
	return len(p), nil
}

// String returns the captured bytes.
func (c *BodyCapture) String() string { return string(c.buf) }

// Truncated reports whether bytes beyond the cap were dropped.
func (c *BodyCapture) Truncated() bool { return c.truncated }

// Size returns the total number of bytes written, including dropped bytes.
func (c *BodyCapture) Size() int { return c.size }

// CapString truncates s to at most limit bytes, reporting whether it
// truncated. A negative limit behaves as zero. Truncation is byte-bounded and
// may split a multi-byte rune.
func CapString(s string, limit int) (out string, truncated bool) {
	if limit < 0 {
		limit = 0
	}
	if len(s) <= limit {
		return s, false
	}
	return s[:limit], true
}
