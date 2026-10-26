package traffic

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestLogCapacityOverflow(t *testing.T) {
	l := New(3)
	for i := range 5 {
		l.Record(&Entry{Method: "POST", Path: "/v1/chat/completions", Model: "m"})
		if got := l.Len(); got > 3 {
			t.Fatalf("Len() = %d after %d records, want <= 3", got, i+1)
		}
	}
	if got := l.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}

	got := l.List(0)
	if len(got) != 3 {
		t.Fatalf("List() returned %d entries, want 3", len(got))
	}
	// Newest-first: IDs 5, 4, 3 survive; 1 and 2 were dropped.
	want := []uint64{5, 4, 3}
	for i, e := range got {
		if e.ID != want[i] {
			t.Errorf("List()[%d].ID = %d, want %d", i, e.ID, want[i])
		}
	}
	prev := got[0].ID
	for _, e := range got[1:] {
		if e.ID >= prev {
			t.Errorf("IDs not newest-first: %d then %d", prev, e.ID)
		}
		prev = e.ID
	}
	if _, ok := l.Get(1); ok {
		t.Error("Get(1) succeeded, want dropped entry")
	}
	if _, ok := l.Get(2); ok {
		t.Error("Get(2) succeeded, want dropped entry")
	}
	if e, ok := l.Get(3); !ok || e.ID != 3 {
		t.Errorf("Get(3) = %v, %v; want entry 3", e, ok)
	}
	if e, ok := l.Get(5); !ok || e.ID != 5 {
		t.Errorf("Get(5) = %v, %v; want entry 5", e, ok)
	}
}

func TestLogIDsMonotonic(t *testing.T) {
	l := New(2)
	for range 10 {
		l.Record(&Entry{})
	}
	entries := l.List(0)
	if len(entries) != 2 {
		t.Fatalf("List() = %d entries, want 2", len(entries))
	}
	if entries[0].ID != 10 || entries[1].ID != 9 {
		t.Errorf("IDs = [%d %d], want [10 9]", entries[0].ID, entries[1].ID)
	}
	if l.nextID != 10 {
		t.Errorf("nextID = %d, want 10", l.nextID)
	}
}

func TestLogListLimit(t *testing.T) {
	l := New(10)
	for range 5 {
		l.Record(&Entry{})
	}
	if got := l.List(2); len(got) != 2 || got[0].ID != 5 || got[1].ID != 4 {
		t.Fatalf("List(2) = %+v, want IDs [5 4]", got)
	}
	if got := l.List(-1); len(got) != 5 {
		t.Fatalf("List(-1) = %d entries, want 5", len(got))
	}
	if got := l.List(99); len(got) != 5 {
		t.Fatalf("List(99) = %d entries, want 5", len(got))
	}
}

func TestNewDefaultCapacity(t *testing.T) {
	if got := New(0).capacity; got != 500 {
		t.Errorf("New(0).capacity = %d, want 500", got)
	}
	if got := New(-7).capacity; got != 500 {
		t.Errorf("New(-7).capacity = %d, want 500", got)
	}
}

func TestRecordAssignsTimeAndID(t *testing.T) {
	l := New(4)
	before := time.Now()
	e := l.Record(&Entry{Method: "POST"})
	after := time.Now()
	if e.ID != 1 || l.Record(&Entry{}).ID != 2 {
		t.Fatalf("IDs = %d, %d; want 1, 2", e.ID, 2)
	}
	if e.Time.Before(before) || e.Time.After(after) {
		t.Errorf("Time %v outside [%v, %v]", e.Time, before, after)
	}

	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	e2 := l.Record(&Entry{Time: fixed})
	if !e2.Time.Equal(fixed) {
		t.Errorf("explicit Time overwritten: %v, want %v", e2.Time, fixed)
	}
}

func TestRecordStoresCopy(t *testing.T) {
	l := New(4)
	input := &Entry{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		RequestBody: `{"a":1}`,
	}
	stored := l.Record(input)

	// Mutating the caller's input must not change stored state.
	input.Method = "GET"
	input.Path = "/mutated"
	input.RequestBody = "tampered"

	got, ok := l.Get(stored.ID)
	if !ok {
		t.Fatalf("Get(%d) failed", stored.ID)
	}
	if got.Method != "POST" || got.Path != "/v1/chat/completions" || got.RequestBody != `{"a":1}` {
		t.Errorf("stored entry mutated through caller: %+v", got)
	}

	// Mutating the returned entry must likewise not affect stored state.
	stored.Method = "DELETE"
	stored.RequestBody = "tampered"
	got2, _ := l.Get(stored.ID)
	if got2.Method != "POST" || got2.RequestBody != `{"a":1}` {
		t.Errorf("stored entry mutated through returned copy: %+v", got2)
	}

	// Mutating a List result must not affect stored state.
	list := l.List(0)
	list[0].Path = "/tampered"
	got3, _ := l.Get(stored.ID)
	if got3.Path != "/v1/chat/completions" {
		t.Errorf("stored entry mutated through List copy: %+v", got3)
	}
}

func TestSubscribeReceivesEntries(t *testing.T) {
	l := New(8)
	ch, unsub := l.Subscribe()
	defer unsub()

	e := l.Record(&Entry{Method: "POST"})
	select {
	case got := <-ch:
		if got.ID != e.ID {
			t.Fatalf("received ID %d, want %d", got.ID, e.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no entry delivered to subscriber")
	}
}

func TestSubscribeSlowDropsOldestAndStaysCurrent(t *testing.T) {
	l := New(2000)
	ch, unsub := l.Subscribe()
	defer unsub()

	const n = 1000
	start := time.Now()
	for range n {
		l.Record(&Entry{Method: "POST"})
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("%d Records took %v, want well under 1s (non-blocking)", n, elapsed)
	}

	// Drain what the subscriber buffered without ever reading during the burst.
	var ids []uint64
	for {
		select {
		case e := <-ch:
			ids = append(ids, e.ID)
			continue
		default:
		}
		break
	}

	if len(ids) == 0 {
		t.Fatal("subscriber received nothing")
	}
	if len(ids) > subBuffer {
		t.Fatalf("buffered %d ids, want <= %d", len(ids), subBuffer)
	}
	// The buffered values are the newest suffix, strictly increasing, ending at
	// the newest recorded entry.
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("ids not strictly increasing at %d: %v", i, ids)
		}
	}
	if last := ids[len(ids)-1]; last != n {
		t.Fatalf("last buffered ID = %d, want newest %d", last, n)
	}
	if first := ids[0]; first != uint64(n-len(ids)+1) {
		t.Fatalf("buffered suffix starts at %d, want %d", first, n-len(ids)+1)
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	l := New(8)
	ch, unsub := l.Subscribe()

	first := l.Record(&Entry{Method: "a"})
	select {
	case got := <-ch:
		if got.ID != first.ID {
			t.Fatalf("received ID %d, want %d", got.ID, first.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no delivery before unsubscribe")
	}

	unsub()
	unsub() // idempotent

	l.Record(&Entry{Method: "b"})
	select {
	case got := <-ch:
		t.Fatalf("received entry %d after unsubscribe", got.ID)
	default:
	}
}

func TestSubscribeUnsubscribeConcurrent(t *testing.T) {
	l := New(256)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publishers.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					l.Record(&Entry{Method: "POST"})
				}
			}
		}()
	}

	// Subscribers churning subscribe/unsubscribe while publishing.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				ch, unsub := l.Subscribe()
				// Drain a little to exercise concurrent delivery.
				select {
				case <-ch:
				default:
				}
				unsub()
				unsub()
			}
		}()
	}

	// Let publishers run until subscribers finish, then run them a bit more.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent test timed out")
	}
}

func TestBodyCaptureUnderLimit(t *testing.T) {
	c := NewBodyCapture(16)
	n, err := c.Write([]byte("hello"))
	if n != 5 || err != nil {
		t.Fatalf("Write = %d, %v; want 5, nil", n, err)
	}
	if c.String() != "hello" {
		t.Errorf("String() = %q, want %q", c.String(), "hello")
	}
	if c.Size() != 5 {
		t.Errorf("Size() = %d, want 5", c.Size())
	}
	if c.Truncated() {
		t.Error("Truncated() = true, want false")
	}
}

func TestBodyCaptureOverLimit(t *testing.T) {
	c := NewBodyCapture(4)
	if n, err := c.Write([]byte("abcdefghij")); n != 10 || err != nil {
		t.Fatalf("Write = %d, %v; want 10, nil", n, err)
	}
	if c.String() != "abcd" {
		t.Errorf("String() = %q, want %q", c.String(), "abcd")
	}
	if c.Size() != 10 {
		t.Errorf("Size() = %d, want 10", c.Size())
	}
	if !c.Truncated() {
		t.Error("Truncated() = false, want true")
	}

	// Multiple writes crossing the cap.
	c2 := NewBodyCapture(5)
	c2.Write([]byte("abc"))
	c2.Write([]byte("def"))
	if c2.String() != "abcde" {
		t.Errorf("String() = %q, want %q", c2.String(), "abcde")
	}
	if c2.Size() != 6 {
		t.Errorf("Size() = %d, want 6", c2.Size())
	}
	if !c2.Truncated() {
		t.Error("Truncated() = false, want true")
	}
}

func TestBodyCaptureExactLimitNotTruncated(t *testing.T) {
	c := NewBodyCapture(4)
	c.Write([]byte("abcd"))
	if c.Truncated() {
		t.Error("Truncated() = true at exactly limit, want false")
	}
	if c.String() != "abcd" || c.Size() != 4 {
		t.Errorf("String()=%q Size()=%d, want abcd/4", c.String(), c.Size())
	}
}

func TestBodyCaptureZeroLimit(t *testing.T) {
	c := NewBodyCapture(0)
	c.Write([]byte("x"))
	if c.String() != "" || !c.Truncated() || c.Size() != 1 {
		t.Errorf("zero-limit capture = %q/%v/%d, want \"\"/true/1", c.String(), c.Truncated(), c.Size())
	}
}

func TestCapStringBoundary(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		limit     int
		wantOut   string
		wantTrunc bool
	}{
		{"under", "abc", 5, "abc", false},
		{"exact", "abcde", 5, "abcde", false},
		{"over", "abcdef", 5, "abcde", true},
		{"empty limit", "abc", 0, "", true},
		{"negative limit", "abc", -1, "", true},
		{"empty string", "", 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, trunc := CapString(tc.in, tc.limit)
			if out != tc.wantOut || trunc != tc.wantTrunc {
				t.Errorf("CapString(%q, %d) = %q, %v; want %q, %v",
					tc.in, tc.limit, out, trunc, tc.wantOut, tc.wantTrunc)
			}
		})
	}
}

func TestEntryJSONWireShape(t *testing.T) {
	// The control API depends on these exact tags for the snake_case wire form.
	l := New(1)
	e := l.Record(&Entry{
		Method:            "POST",
		Path:              "/v1/chat/completions",
		Model:             "m",
		Status:            200,
		DurationMS:        12,
		RequestSize:       3,
		ResponseSize:      4,
		FilterSummary:     "redact-keys replace x1",
		NotFiltered:       true,
		Blocked:           false,
		RequestBody:       "{}",
		RequestTruncated:  true,
		ResponseBody:      "ok",
		ResponseTruncated: true,
	})
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{
		"id", "time", "method", "path", "model", "status", "duration_ms",
		"request_size", "response_size", "filter_summary",
		"not_content_filtered", "request_body", "request_truncated",
		"response_body", "response_truncated",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("marshaled entry missing key %q", key)
		}
	}
	if _, ok := m["blocked"]; ok {
		t.Error("marshaled entry includes blocked=true despite omitempty/false")
	}
}
