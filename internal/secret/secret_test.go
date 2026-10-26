package secret_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mickstar/llm-proxy/internal/secret"
)

func TestParseRef(t *testing.T) {
	service, account, err := secret.ParseRef("{keychain:FireworksAI/fireworks-api-key}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if service != "FireworksAI" || account != "fireworks-api-key" {
		t.Fatalf("got %q/%q", service, account)
	}
}

func TestParseRefRejectsMalformed(t *testing.T) {
	cases := []string{
		"fw_literal-secret",
		"{keychain:FireworksAI/fireworks-api-key",
		"{keychain:}",
		"{keychain:/fireworks-api-key}",
		"{keychain:FireworksAI/}",
		"{keychain:FireworksAI/a/b}",
	}
	for _, in := range cases {
		if _, _, err := secret.ParseRef(in); err == nil {
			t.Errorf("ParseRef(%q) accepted a malformed reference", in)
		}
	}
}

func TestResolvePassesLiteralsThrough(t *testing.T) {
	r := secret.NewResolver(secret.WithLookup(func(string, string) (string, error) {
		t.Fatal("lookup called for a literal value")
		return "", nil
	}))
	got, err := r.Resolve("fw_literal")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "fw_literal" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveReadsKeychainOncePerTTL(t *testing.T) {
	var calls int
	var gotService, gotAccount string
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := secret.NewResolver(
		secret.WithTTL(time.Minute),
		secret.WithClock(func() time.Time { return now }),
		secret.WithLookup(func(service, account string) (string, error) {
			calls++
			gotService, gotAccount = service, account
			return "fw_secret", nil
		}),
	)
	ref := "{keychain:FireworksAI/fireworks-api-key}"
	for i := range 3 {
		v, err := r.Resolve(ref)
		if err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
		if v != "fw_secret" {
			t.Fatalf("got %q", v)
		}
	}
	if calls != 1 {
		t.Fatalf("lookup ran %d times; want 1", calls)
	}
	if gotService != "FireworksAI" || gotAccount != "fireworks-api-key" {
		t.Fatalf("lookup got %q/%q", gotService, gotAccount)
	}

	now = now.Add(61 * time.Second)
	if _, err := r.Resolve(ref); err != nil {
		t.Fatalf("resolve after expiry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("lookup ran %d times after expiry; want 2", calls)
	}
}

func TestResolveNegativeTTLDisablesCache(t *testing.T) {
	var calls int
	r := secret.NewResolver(
		secret.WithTTL(-1),
		secret.WithLookup(func(string, string) (string, error) {
			calls++
			return "fw_secret", nil
		}),
	)
	for range 2 {
		if _, err := r.Resolve("{keychain:S/A}"); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("lookup ran %d times; want 2", calls)
	}
}

func TestResolveDoesNotCacheFailures(t *testing.T) {
	var calls int
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	r := secret.NewResolver(
		secret.WithClock(func() time.Time { return now }),
		secret.WithLookup(func(string, string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("item not found")
			}
			return "fw_secret", nil
		}),
	)
	ref := "{keychain:FireworksAI/fireworks-api-key}"
	if _, err := r.Resolve(ref); err == nil {
		t.Fatal("first resolve succeeded; want the lookup error")
	}
	v, err := r.Resolve(ref)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if v != "fw_secret" {
		t.Fatalf("got %q", v)
	}
}

func TestResolvePropagatesLookupError(t *testing.T) {
	r := secret.NewResolver(secret.WithLookup(func(string, string) (string, error) {
		return "", errors.New("keychain locked")
	}))
	_, err := r.Resolve("{keychain:FireworksAI/fireworks-api-key}")
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("err = %v; want the lookup error", err)
	}
}

func TestResolveRejectsMalformedRef(t *testing.T) {
	r := secret.NewResolver(secret.WithLookup(func(string, string) (string, error) {
		t.Fatal("lookup called for a malformed reference")
		return "", nil
	}))
	if _, err := r.Resolve("{keychain:FireworksAI}"); err == nil {
		t.Fatal("malformed reference resolved")
	}
}
