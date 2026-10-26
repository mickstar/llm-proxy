package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// WatchInterval is how often the manager polls the config file for external
// changes (mtime+size first, then content hash).
const WatchInterval = 500 * time.Millisecond

// Manager owns the config file: it holds the current immutable snapshot,
// serializes all writes through Update (validate -> atomic save -> swap), and
// watches for external edits, applying valid ones live and keeping the
// last-good config on invalid ones.
type Manager struct {
	path string

	mu        sync.Mutex // serializes Update and reload
	cur       atomic.Pointer[Config]
	hash      atomic.Pointer[[32]byte]
	lastMod   time.Time
	lastSize  int64
	errMu     sync.RWMutex
	watchErr  error
	subsMu    sync.RWMutex
	subs      map[uint64]func(*Config)
	nextSubID uint64

	stop      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// Open loads (creating defaults if absent) the config at path and starts
// watching it for external changes.
func Open(path string) (*Manager, error) {
	c, err := Load(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	m := &Manager{
		path: path,
		stop: make(chan struct{}),
		subs: map[uint64]func(*Config){},
	}
	m.cur.Store(c)
	m.setHash(data)
	if info, err := os.Stat(path); err == nil {
		m.lastMod, m.lastSize = info.ModTime(), info.Size()
	}
	m.wg.Add(1)
	go m.watch()
	return m, nil
}

// Path returns the config file path.
func (m *Manager) Path() string { return m.path }

// Current returns the current config snapshot. The returned value must be
// treated as immutable; use Update to change configuration.
func (m *Manager) Current() *Config { return m.cur.Load() }

// Update applies fn to a copy of the current config, validates it, atomically
// saves it, and swaps the in-memory snapshot. Subscribers are notified once on
// success. On error nothing changes.
func (m *Manager) Update(fn func(*Config) error) error {
	m.mu.Lock()
	clone := m.cur.Load().Clone()
	if err := fn(clone); err != nil {
		m.mu.Unlock()
		return err
	}
	clone.normalize()
	data, err := Save(m.path, clone)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.cur.Store(clone)
	m.setHash(data)
	if info, err := os.Stat(m.path); err == nil {
		m.lastMod, m.lastSize = info.ModTime(), info.Size()
	}
	m.mu.Unlock()

	m.setWatchErr(nil)
	m.notify(clone)
	return nil
}

// WatchError returns the error from the most recent invalid external edit, or
// nil when the on-disk config is valid.
func (m *Manager) WatchError() error {
	m.errMu.RLock()
	defer m.errMu.RUnlock()
	return m.watchErr
}

// OnChange registers fn to run after each successful reload or update. The
// returned function unsubscribes.
func (m *Manager) OnChange(fn func(*Config)) func() {
	m.subsMu.Lock()
	id := m.nextSubID
	m.nextSubID++
	m.subs[id] = fn
	m.subsMu.Unlock()
	return func() {
		m.subsMu.Lock()
		delete(m.subs, id)
		m.subsMu.Unlock()
	}
}

// Close stops the file watcher.
func (m *Manager) Close() {
	m.closeOnce.Do(func() { close(m.stop) })
	m.wg.Wait()
}

func (m *Manager) setHash(data []byte) {
	h := sha256.Sum256(data)
	m.hash.Store(&h)
}

func (m *Manager) setWatchErr(err error) {
	m.errMu.Lock()
	m.watchErr = err
	m.errMu.Unlock()
}

func (m *Manager) notify(c *Config) {
	m.subsMu.RLock()
	fns := make([]func(*Config), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.subsMu.RUnlock()
	for _, fn := range fns {
		fn(c)
	}
}

func (m *Manager) watch() {
	defer m.wg.Done()
	t := time.NewTicker(WatchInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.poll()
		}
	}
}

// poll checks the file's mtime+size and, when changed, applies the new contents
// if they differ from the last known hash and validate.
func (m *Manager) poll() {
	info, err := os.Stat(m.path)
	if err != nil {
		return
	}
	m.mu.Lock()
	if info.ModTime().Equal(m.lastMod) && info.Size() == m.lastSize {
		m.mu.Unlock()
		return
	}
	m.lastMod, m.lastSize = info.ModTime(), info.Size()
	m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	if h := m.hash.Load(); h != nil && *h == sha256.Sum256(data) {
		return
	}
	c, err := decode(data)
	if err != nil {
		m.setWatchErr(fmt.Errorf("config %s: %w", m.path, err))
		return
	}
	if err := c.Validate(); err != nil {
		m.setWatchErr(fmt.Errorf("config %s: %w", m.path, err))
		return
	}
	m.mu.Lock()
	m.cur.Store(c)
	m.setHash(data)
	m.mu.Unlock()
	m.setWatchErr(nil)
	m.notify(c)
}
