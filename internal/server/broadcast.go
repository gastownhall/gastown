package server

import "sync"

// broadcaster wakes every subscriber, for dashboard SSE streams. Each
// subscriber has a 1-buffered channel, so notify never blocks and wakes that
// arrive while a subscriber is busy coalesce into one.
type broadcaster struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

func newBroadcaster() *broadcaster {
	return &broadcaster{subs: map[chan struct{}]struct{}{}}
}

// subscribe registers a subscriber. Call the returned func to remove it.
func (b *broadcaster) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *broadcaster) notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// count returns the number of subscribers.
func (b *broadcaster) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
