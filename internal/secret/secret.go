// Package secret resolves the upstream credential. A config value may be a
// literal secret or a reference to a macOS keychain item, so a key managed by
// another tool never has to be copied into the config file.
//
// References look like "{keychain:service/account}" and are resolved on demand.
// Resolved values are cached so a keychain read does not run on every proxied
// request, and a rotated key is picked up once the cache expires.
package secret

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// RefPrefix starts a keychain reference.
const RefPrefix = "{keychain:"

// DefaultTTL is how long a resolved keychain value is reused before the item is
// read again.
const DefaultTTL = time.Minute

// lookupTimeout bounds one keychain read so a locked keychain cannot stall a
// proxied request.
const lookupTimeout = 10 * time.Second

// LookupFunc reads a secret from an external store.
type LookupFunc func(service, account string) (string, error)

// Resolver turns config values into secrets.
type Resolver struct {
	lookup LookupFunc
	ttl    time.Duration
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value   string
	expires time.Time
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithLookup replaces the keychain reader, which is otherwise Keychain.
func WithLookup(f LookupFunc) Option {
	return func(r *Resolver) { r.lookup = f }
}

// WithTTL sets the cache lifetime. A negative TTL disables caching; a zero TTL
// keeps DefaultTTL.
func WithTTL(d time.Duration) Option {
	return func(r *Resolver) { r.ttl = d }
}

// WithClock replaces the clock used for cache expiry.
func WithClock(f func() time.Time) Option {
	return func(r *Resolver) { r.now = f }
}

// NewResolver returns a resolver over the macOS keychain.
func NewResolver(opts ...Option) *Resolver {
	r := &Resolver{
		lookup: Keychain,
		ttl:    DefaultTTL,
		now:    time.Now,
		cache:  map[string]cacheEntry{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// IsRef reports whether value is a keychain reference rather than a literal
// secret.
func IsRef(value string) bool {
	return strings.HasPrefix(value, RefPrefix)
}

// ParseRef splits "{keychain:service/account}" into its service and account.
func ParseRef(value string) (service, account string, err error) {
	const shape = `want {keychain:service/account}`
	rest, ok := strings.CutPrefix(value, RefPrefix)
	if !ok {
		return "", "", fmt.Errorf("not a keychain reference: %q", value)
	}
	inner, ok := strings.CutSuffix(rest, "}")
	if !ok {
		return "", "", fmt.Errorf("keychain reference %q is missing the closing brace: %s", value, shape)
	}
	service, account, ok = strings.Cut(inner, "/")
	if !ok || service == "" || account == "" || strings.Contains(account, "/") {
		return "", "", fmt.Errorf("keychain reference %q is malformed: %s", value, shape)
	}
	return service, account, nil
}

// Resolve returns the secret named by value. A literal value is returned
// unchanged; a reference is looked up and cached.
func (r *Resolver) Resolve(value string) (string, error) {
	if !IsRef(value) {
		return value, nil
	}
	service, account, err := ParseRef(value)
	if err != nil {
		return "", err
	}
	// Serialize lookups so concurrent requests for the same item share one
	// keychain read instead of racing.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ttl >= 0 {
		if e, ok := r.cache[value]; ok && r.now().Before(e.expires) {
			return e.value, nil
		}
	}
	v, err := r.lookup(service, account)
	if err != nil {
		return "", err
	}
	if r.ttl > 0 {
		r.cache[value] = cacheEntry{value: v, expires: r.now().Add(r.ttl)}
	}
	return v, nil
}

// Keychain reads a generic password from the macOS keychain with `security`.
// The command is non-interactive for items whose access control allows the
// caller; a locked or protected item fails with the tool's message.
func Keychain(service, account string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("keychain references are only supported on macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			msg := strings.TrimSpace(string(exit.Stderr))
			if msg == "" {
				msg = exit.Error()
			}
			return "", fmt.Errorf("keychain item %s/%s: %s", service, account, msg)
		}
		return "", fmt.Errorf("keychain item %s/%s: %w", service, account, err)
	}
	v := strings.TrimRight(string(out), "\r\n")
	if v == "" {
		return "", fmt.Errorf("keychain item %s/%s is empty", service, account)
	}
	return v, nil
}
