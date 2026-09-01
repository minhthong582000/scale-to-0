package activity

import (
	"testing"
	"time"
)

// newTestStore returns a store whose clock the test controls.
func newTestStore(window time.Duration) (*Store, *time.Time) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s := New(window)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		in   string
		want Key
		ok   bool
	}{
		{"demo/webapp", Key{"demo", "webapp"}, true},
		{"demo/", Key{}, false},
		{"/webapp", Key{}, false},
		{"webapp", Key{}, false},
		{"", Key{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseKey(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseKey(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// A proxy's first report establishes a baseline. Counting it as traffic would
// wake a workload that has been idle since before the scaler started.
func TestObserveFirstSampleIsBaselineOnly(t *testing.T) {
	s, _ := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 500)

	if s.Active(key, time.Minute) {
		t.Error("workload is active after only a baseline sample")
	}
	if got := s.RequestsPerSecond(key); got != 0 {
		t.Errorf("RequestsPerSecond = %v, want 0", got)
	}
}

func TestObserveCountsIncrements(t *testing.T) {
	s, _ := newTestStore(10 * time.Second)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 100)
	s.Observe(key, "node-1", 130)

	if !s.Active(key, time.Minute) {
		t.Fatal("workload is not active after traffic")
	}
	if got, want := s.RequestsPerSecond(key), 3.0; got != want {
		t.Errorf("RequestsPerSecond = %v, want %v", got, want)
	}
}

// A restarted proxy reports a counter lower than its last one. The drop is not
// negative traffic; the new value is the traffic since the restart.
func TestObserveHandlesCounterReset(t *testing.T) {
	s, _ := newTestStore(10 * time.Second)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 100)
	s.Observe(key, "node-1", 150)
	s.Observe(key, "node-1", 20)

	if got, want := s.RequestsPerSecond(key), 7.0; got != want {
		t.Errorf("RequestsPerSecond = %v, want %v", got, want)
	}
}

// Counters are per proxy. Deltas must be taken per node, or a new pod's
// baseline would look like a burst of traffic.
func TestObserveAggregatesAcrossNodes(t *testing.T) {
	s, _ := newTestStore(10 * time.Second)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 0)
	s.Observe(key, "node-2", 1000)
	s.Observe(key, "node-1", 10)
	s.Observe(key, "node-2", 1020)

	if got, want := s.RequestsPerSecond(key), 3.0; got != want {
		t.Errorf("RequestsPerSecond = %v, want %v", got, want)
	}
}

func TestRequestsPerSecondDropsSamplesOutsideWindow(t *testing.T) {
	s, now := newTestStore(10 * time.Second)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 0)
	s.Observe(key, "node-1", 50)
	if got, want := s.RequestsPerSecond(key), 5.0; got != want {
		t.Fatalf("RequestsPerSecond = %v, want %v", got, want)
	}

	*now = now.Add(11 * time.Second)
	if got := s.RequestsPerSecond(key); got != 0 {
		t.Errorf("RequestsPerSecond after window = %v, want 0", got)
	}
}

func TestActiveExpiresAfterIdle(t *testing.T) {
	s, now := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	s.Observe(key, "node-1", 0)
	s.Observe(key, "node-1", 1)
	if !s.Active(key, 30*time.Second) {
		t.Fatal("workload is not active immediately after traffic")
	}

	*now = now.Add(31 * time.Second)
	if s.Active(key, 30*time.Second) {
		t.Error("workload is still active past the idle window")
	}
}

func TestActiveUnknownWorkload(t *testing.T) {
	s, _ := newTestStore(time.Minute)
	if s.Active(Key{"demo", "missing"}, time.Minute) {
		t.Error("unknown workload reported active")
	}
	if _, ok := s.LastRequest(Key{"demo", "missing"}); ok {
		t.Error("unknown workload reported a last request")
	}
}

func TestSubscribeNotifiesOnTraffic(t *testing.T) {
	s, _ := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	ch, cancel := s.Subscribe(key)
	defer cancel()

	s.Observe(key, "node-1", 0) // baseline: no traffic, no notification
	select {
	case <-ch:
		t.Fatal("notified on a baseline sample")
	default:
	}

	s.Observe(key, "node-1", 1)
	select {
	case <-ch:
	default:
		t.Fatal("no notification after traffic")
	}
}

// A subscriber that is not reading must never block the metrics stream.
func TestSubscribeCoalescesNotifications(t *testing.T) {
	s, _ := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	ch, cancel := s.Subscribe(key)
	defer cancel()

	s.Observe(key, "node-1", 0)
	for i := 1; i <= 5; i++ {
		s.Observe(key, "node-1", float64(i))
	}

	if len(ch) != 1 {
		t.Errorf("pending notifications = %d, want 1", len(ch))
	}
}

func TestCancelSubscriptionStopsNotifications(t *testing.T) {
	s, _ := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	ch, cancel := s.Subscribe(key)
	s.Observe(key, "node-1", 0)
	cancel()
	s.Observe(key, "node-1", 5)

	select {
	case <-ch:
		t.Error("notified after cancelling the subscription")
	default:
	}
	if len(s.subs) != 0 {
		t.Errorf("subscriber map still holds %d keys", len(s.subs))
	}
}

// Pods are recreated constantly; their counter state must not accumulate.
func TestNodeStateExpires(t *testing.T) {
	s, now := newTestStore(time.Minute)
	key := Key{"demo", "webapp"}

	s.Observe(key, "old-node", 10)
	*now = now.Add(nodeTTL + time.Second)
	s.Observe(key, "new-node", 10)

	if _, ok := s.nodes["old-node"]; ok {
		t.Error("expired node state was not pruned")
	}
	if _, ok := s.nodes["new-node"]; !ok {
		t.Error("current node state was pruned")
	}
}
