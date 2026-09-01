// Package activity tracks when each workload last served traffic.
//
// Envoy proxies stream their stats to the collector, which reports the running
// request counters here. The store turns those cumulative counters into two
// answers the autoscaler needs: has this workload seen a request recently, and
// how many requests per second is it taking.
package activity

import (
	"sync"
	"time"
)

// nodeTTL is how long a proxy's counter state is kept after it stops
// reporting. Pods come and go; without this their state would accumulate.
const nodeTTL = 5 * time.Minute

// Key identifies a scalable workload, not an individual pod.
type Key struct {
	Namespace string
	Name      string
}

func (k Key) String() string { return k.Namespace + "/" + k.Name }

// ParseKey reads a "namespace/name" pair.
func ParseKey(s string) (Key, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			ns, name := s[:i], s[i+1:]
			if ns == "" || name == "" {
				return Key{}, false
			}
			return Key{Namespace: ns, Name: name}, true
		}
	}
	return Key{}, false
}

type sample struct {
	at    time.Time
	count float64
}

type nodeState struct {
	total    float64
	lastSeen time.Time
}

type workload struct {
	lastRequest time.Time
	samples     []sample
}

// Store holds per-workload request activity. It is safe for concurrent use.
type Store struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time

	workloads map[Key]*workload
	nodes     map[string]*nodeState
	subs      map[Key]map[chan struct{}]struct{}
}

// New returns a store that averages request rates over window.
func New(window time.Duration) *Store {
	return &Store{
		window:    window,
		now:       time.Now,
		workloads: make(map[Key]*workload),
		nodes:     make(map[string]*nodeState),
		subs:      make(map[Key]map[chan struct{}]struct{}),
	}
}

// Observe reports the cumulative request count for one proxy. Counters are
// per-proxy, so the delta is taken per node and then credited to the workload:
// that keeps the rate correct when pods are added or removed.
func (s *Store) Observe(key Key, nodeID string, total float64) {
	s.mu.Lock()
	now := s.now()

	node, ok := s.nodes[nodeID]
	if !ok {
		node = &nodeState{}
		s.nodes[nodeID] = node
	}

	var delta float64
	switch {
	case !ok:
		// First report from this proxy. Its counter may already be well above
		// zero, and crediting all of it would invent traffic that happened
		// before we were listening, so take this sample as the baseline.
		delta = 0
	case total < node.total:
		// The proxy restarted and its counters reset.
		delta = total
	default:
		delta = total - node.total
	}
	node.total = total
	node.lastSeen = now

	if delta > 0 {
		w, ok := s.workloads[key]
		if !ok {
			w = &workload{}
			s.workloads[key] = w
		}
		w.lastRequest = now
		w.samples = append(prune(w.samples, now.Add(-s.window)), sample{at: now, count: delta})
	}

	s.pruneNodes(now)
	s.mu.Unlock()

	if delta > 0 {
		s.notify(key)
	}
}

// RequestsPerSecond is the average rate over the store's window.
func (s *Store) RequestsPerSecond(key Key) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.workloads[key]
	if !ok {
		return 0
	}
	w.samples = prune(w.samples, s.now().Add(-s.window))

	var total float64
	for _, sm := range w.samples {
		total += sm.count
	}
	return total / s.window.Seconds()
}

// Active reports whether the workload served a request within idle.
func (s *Store) Active(key Key, idle time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.workloads[key]
	if !ok {
		return false
	}
	return s.now().Sub(w.lastRequest) < idle
}

// LastRequest returns when the workload last served a request.
func (s *Store) LastRequest(key Key) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	w, ok := s.workloads[key]
	if !ok {
		return time.Time{}, false
	}
	return w.lastRequest, true
}

// Subscribe returns a channel that receives a value whenever the workload sees
// traffic. The channel has room for one pending notification, so a slow reader
// coalesces rather than blocking the metrics stream. Call cancel to release it.
func (s *Store) Subscribe(key Key) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	s.mu.Lock()
	if s.subs[key] == nil {
		s.subs[key] = make(map[chan struct{}]struct{})
	}
	s.subs[key][ch] = struct{}{}
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if subs, ok := s.subs[key]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(s.subs, key)
			}
		}
	}
}

func (s *Store) notify(key Key) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for ch := range s.subs[key] {
		select {
		case ch <- struct{}{}:
		default: // a notification is already pending
		}
	}
}

func (s *Store) pruneNodes(now time.Time) {
	for id, n := range s.nodes {
		if now.Sub(n.lastSeen) > nodeTTL {
			delete(s.nodes, id)
		}
	}
}

func prune(samples []sample, cutoff time.Time) []sample {
	i := 0
	for i < len(samples) && !samples[i].at.After(cutoff) {
		i++
	}
	return samples[i:]
}
