// Package stats aggregates token usage and request activity across proxied
// exchanges. It keeps all-time totals (persisted to disk) plus rolling
// one-second and one-minute windows for throughput and recent activity, so a
// lightweight frontend such as the macOS menu bar app can poll one endpoint.
package stats

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mickstar/llm-proxy/internal/pricing"
)

const (
	// windowSeconds is the trailing window used for throughput rates.
	windowSeconds = 60
	// activityMinutes is how many per-minute buckets a snapshot exposes.
	activityMinutes = 60
	// flushInterval bounds how much data a crash can lose.
	flushInterval = 5 * time.Second
	// fileVersion identifies the persisted format.
	fileVersion = 1
)

// Event is one completed proxied exchange as seen by the metrics store.
type Event struct {
	Time               time.Time
	Model              string
	PromptTokens       int
	CachedPromptTokens int
	CompletionTokens   int
	TotalTokens        int
	CostUSD            float64
	Status             int
	Blocked            bool
	RequestBytes       int
	ResponseBytes      int
	DurationMS         int64
}

// Totals counts requests and tokens. Errors counts responses with status >=
// 400; Blocked counts requests refused by a content filter.
type Totals struct {
	Requests           int64   `json:"requests"`
	PromptTokens       int64   `json:"prompt_tokens"`
	CachedPromptTokens int64   `json:"cached_prompt_tokens"`
	CompletionTokens   int64   `json:"completion_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	CostUSD            float64 `json:"cost_usd"`
	Errors             int64   `json:"errors"`
	Blocked            int64   `json:"blocked"`
	RequestBytes       int64   `json:"request_bytes"`
	ResponseBytes      int64   `json:"response_bytes"`
}

// ModelTotals is the all-time accounting for one model id.
type ModelTotals struct {
	Model string `json:"model"`
	Totals
	LastUsed time.Time `json:"last_used,omitzero"`
}

// Throughput holds the trailing-window rates. Token rates count completion
// tokens only: prompt tokens are read, not generated.
type Throughput struct {
	WindowSeconds             int     `json:"window_seconds"`
	CompletionTokensPerSecond float64 `json:"completion_tokens_per_second"`
	TotalTokensPerSecond      float64 `json:"total_tokens_per_second"`
	RequestsPerMinute         float64 `json:"requests_per_minute"`
}

// ActivityBucket is one minute of recent traffic.
type ActivityBucket struct {
	Minute   time.Time `json:"minute"`
	Requests int64     `json:"requests"`
	Tokens   int64     `json:"tokens"`
}

// LastRequest describes the most recent model request that returned usage.
type LastRequest struct {
	At               time.Time `json:"at"`
	Model            string    `json:"model"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	DurationMS       int64     `json:"duration_ms"`
	TokensPerSecond  float64   `json:"tokens_per_second"`
}

// Snapshot is the serializable state served by the control API.
type Snapshot struct {
	Since          time.Time        `json:"since,omitzero"`
	UptimeSeconds  int64            `json:"uptime_seconds"`
	ActiveRequests int64            `json:"active_requests"`
	Totals         Totals           `json:"totals"`
	Models         []ModelTotals    `json:"models"`
	Throughput     Throughput       `json:"throughput"`
	Activity       []ActivityBucket `json:"activity"`
	Last           *LastRequest     `json:"last,omitempty"`
	PersistError   string           `json:"persist_error,omitempty"`
}

// Store is the concurrency-safe metrics aggregator.
type Store struct {
	mu      sync.Mutex
	path    string
	started time.Time

	since  time.Time
	totals Totals
	models map[string]*ModelTotals

	seconds [windowSeconds]secondBucket
	minutes [activityMinutes]minuteBucket

	active  int64
	last    LastRequest
	hasLast bool

	dirty      bool
	costSeeded bool
	saveErr    string
	stop       chan struct{}
	done       chan struct{}
	closeOne   sync.Once
}

type secondBucket struct {
	sec         int64
	requests    int64
	completion  int64
	totalTokens int64
}

type minuteBucket struct {
	minute   int64
	requests int64
	tokens   int64
}

// New loads the persisted totals at path (when it exists) and returns a store.
// A non-empty path starts a background flusher. An unreadable or corrupt file
// is ignored: metrics are auxiliary and must never keep the proxy from
// starting.
func New(path string) *Store {
	s := &Store{
		path:    path,
		started: time.Now(),
		models:  make(map[string]*ModelTotals),
	}
	s.load()
	if path != "" {
		s.stop = make(chan struct{})
		s.done = make(chan struct{})
		go s.flushLoop()
	}
	return s
}

// BackfillCosts estimates costs for totals that predate cost tracking, using
// per-token catalog rates. It runs once per store: entries that already carry a
// cost are left alone. Prices are matched exactly first, then by the longest
// id prefix ending at a "-" or ":" boundary, so a dated variant such as
// "deepseek/deepseek-v4.1-flash-1017" inherits its base model's rates. History
// records no cached-token split, so every prompt token bills at the prompt
// rate.
func (s *Store) BackfillCosts(prices pricing.Table) {
	if s == nil || len(prices) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.costSeeded {
		return
	}
	var added float64
	matched := false
	for id, m := range s.models {
		if m.CostUSD != 0 || m.TotalTokens == 0 {
			continue
		}
		rate, ok := prices.Lookup(id)
		if !ok {
			continue
		}
		matched = true
		m.CostUSD = float64(m.PromptTokens)*rate.Prompt + float64(m.CompletionTokens)*rate.Completion
		added += m.CostUSD
	}
	if !matched {
		return
	}
	s.totals.CostUSD += added
	s.costSeeded = true
	s.dirty = true
}

// Begin marks one request as in flight. Every Begin must be paired with End.
func (s *Store) Begin() {
	s.mu.Lock()
	s.active++
	s.mu.Unlock()
}

// End marks one in-flight request as finished.
func (s *Store) End() {
	s.mu.Lock()
	if s.active > 0 {
		s.active--
	}
	s.mu.Unlock()
}

// Record adds one completed exchange. A zero Time is replaced with now, and a
// zero TotalTokens is derived from the prompt and completion counts.
func (s *Store) Record(e Event) {
	if s == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.TotalTokens == 0 {
		e.TotalTokens = e.PromptTokens + e.CompletionTokens
	}
	if e.CompletionTokens == 0 {
		e.CompletionTokens = e.TotalTokens - e.PromptTokens
	}
	if e.CompletionTokens < 0 {
		e.CompletionTokens = 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.since.IsZero() {
		s.since = e.Time
	}
	s.totals.Requests++
	s.totals.PromptTokens += int64(e.PromptTokens)
	s.totals.CachedPromptTokens += int64(e.CachedPromptTokens)
	s.totals.CompletionTokens += int64(e.CompletionTokens)
	s.totals.TotalTokens += int64(e.TotalTokens)
	s.totals.CostUSD += e.CostUSD
	s.totals.RequestBytes += int64(e.RequestBytes)
	s.totals.ResponseBytes += int64(e.ResponseBytes)
	if e.Status >= 400 {
		s.totals.Errors++
	}
	if e.Blocked {
		s.totals.Blocked++
	}

	if e.Model != "" {
		m := s.models[e.Model]
		if m == nil {
			m = &ModelTotals{Model: e.Model}
			s.models[e.Model] = m
		}
		m.Requests++
		m.PromptTokens += int64(e.PromptTokens)
		m.CachedPromptTokens += int64(e.CachedPromptTokens)
		m.CompletionTokens += int64(e.CompletionTokens)
		m.TotalTokens += int64(e.TotalTokens)
		m.CostUSD += e.CostUSD
		m.RequestBytes += int64(e.RequestBytes)
		m.ResponseBytes += int64(e.ResponseBytes)
		if e.Status >= 400 {
			m.Errors++
		}
		if e.Blocked {
			m.Blocked++
		}
		m.LastUsed = e.Time
	}

	s.bumpSecond(e)
	s.bumpMinute(e)

	if e.Model != "" && e.TotalTokens > 0 {
		s.last = LastRequest{
			At:               e.Time,
			Model:            e.Model,
			CompletionTokens: e.CompletionTokens,
			TotalTokens:      e.TotalTokens,
			CostUSD:          e.CostUSD,
			DurationMS:       e.DurationMS,
		}
		if e.DurationMS > 0 {
			s.last.TokensPerSecond = float64(e.CompletionTokens) / (float64(e.DurationMS) / 1000)
		}
		s.hasLast = true
	}
	s.dirty = true
}

func (s *Store) bumpSecond(e Event) {
	sec := e.Time.Unix()
	b := &s.seconds[mod(sec, windowSeconds)]
	if b.sec != sec {
		*b = secondBucket{sec: sec}
	}
	b.requests++
	b.completion += int64(e.CompletionTokens)
	b.totalTokens += int64(e.TotalTokens)
}

func (s *Store) bumpMinute(e Event) {
	minute := e.Time.Unix() / 60
	b := &s.minutes[mod(minute, activityMinutes)]
	if b.minute != minute {
		*b = minuteBucket{minute: minute}
	}
	b.requests++
	b.tokens += int64(e.TotalTokens)
}

func mod(a, m int64) int {
	r := a % m
	if r < 0 {
		r += m
	}
	return int(r)
}

// Snapshot returns the current state relative to the wall clock.
func (s *Store) Snapshot() Snapshot {
	return s.SnapshotAt(time.Now())
}

// SnapshotAt returns the current state with rolling windows evaluated at now.
func (s *Store) SnapshotAt(now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := Snapshot{
		Since:          s.since,
		UptimeSeconds:  int64(now.Sub(s.started).Seconds()),
		ActiveRequests: s.active,
		Totals:         s.totals,
		Models:         make([]ModelTotals, 0, len(s.models)),
		PersistError:   s.saveErr,
	}
	if snap.UptimeSeconds < 0 {
		snap.UptimeSeconds = 0
	}
	for _, m := range s.models {
		snap.Models = append(snap.Models, *m)
	}
	sort.Slice(snap.Models, func(i, j int) bool {
		a, b := snap.Models[i], snap.Models[j]
		if a.TotalTokens != b.TotalTokens {
			return a.TotalTokens > b.TotalTokens
		}
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		return a.Model < b.Model
	})

	var req, comp, total int64
	oldest := now.Unix() - windowSeconds + 1
	for _, b := range s.seconds {
		if b.sec < oldest || b.sec > now.Unix() {
			continue
		}
		req += b.requests
		comp += b.completion
		total += b.totalTokens
	}
	snap.Throughput = Throughput{
		WindowSeconds:             windowSeconds,
		CompletionTokensPerSecond: float64(comp) / windowSeconds,
		TotalTokensPerSecond:      float64(total) / windowSeconds,
		RequestsPerMinute:         float64(req) / windowSeconds * 60,
	}

	snap.Activity = make([]ActivityBucket, activityMinutes)
	nowMinute := now.Unix() / 60
	for i := range activityMinutes {
		minute := nowMinute - int64(activityMinutes-1-i)
		snap.Activity[i].Minute = time.Unix(minute*60, 0).UTC()
		b := s.minutes[mod(minute, activityMinutes)]
		if b.minute == minute {
			snap.Activity[i].Requests = b.requests
			snap.Activity[i].Tokens = b.tokens
		}
	}

	if s.hasLast {
		last := s.last
		snap.Last = &last
	}
	return snap
}

// Close stops the flusher and writes the persisted totals.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.closeOne.Do(func() {
		if s.stop != nil {
			close(s.stop)
			<-s.done
		}
	})
	return s.flush()
}

func (s *Store) flushLoop() {
	defer close(s.done)
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			_ = s.flush()
		}
	}
}

// flush persists the all-time totals when they changed.
func (s *Store) flush() error {
	if s.path == "" {
		return nil
	}

	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	p := persisted{
		Version:    fileVersion,
		Since:      s.since,
		UpdatedAt:  time.Now(),
		Totals:     s.totals,
		Models:     make(map[string]ModelTotals, len(s.models)),
		CostSeeded: s.costSeeded,
	}
	for id, m := range s.models {
		p.Models[id] = *m
	}
	s.dirty = false
	s.mu.Unlock()

	err := writeFile(s.path, p)
	s.mu.Lock()
	if err != nil {
		s.saveErr = err.Error()
		s.dirty = true
	} else {
		s.saveErr = ""
	}
	s.mu.Unlock()
	return err
}

func (s *Store) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil || p.Version != fileVersion {
		return
	}
	s.since = p.Since
	s.totals = p.Totals
	s.costSeeded = p.CostSeeded
	for id, m := range p.Models {
		if id == "" {
			continue
		}
		m.Model = id
		s.models[id] = &m
	}
}

type persisted struct {
	Version    int                    `json:"version"`
	Since      time.Time              `json:"since,omitzero"`
	UpdatedAt  time.Time              `json:"updated_at,omitzero"`
	Totals     Totals                 `json:"totals"`
	Models     map[string]ModelTotals `json:"models,omitempty"`
	CostSeeded bool                   `json:"cost_seeded,omitempty"`
}

// writeFile replaces path atomically with the JSON encoding of v.
func writeFile(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create stats dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".stats-*.json")
	if err != nil {
		return fmt.Errorf("create stats file: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode stats: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod stats: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close stats: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace stats: %w", err)
	}
	return nil
}
