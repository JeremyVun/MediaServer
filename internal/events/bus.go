package events

import "sync"

// subscriberBuffer is how many events a subscriber can lag before its
// oldest pending event is dropped.
const subscriberBuffer = 64

type Bus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	nextID int
	closed bool
}

func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe returns a channel of events and a cancel function. The channel
// is closed on cancel (or bus Close); cancel is idempotent.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	ch := make(chan Event, subscriberBuffer)
	b.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if sub, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub)
			}
		})
	}
	return ch, cancel
}

// Publish delivers to every subscriber without ever blocking: if a
// subscriber's buffer is full, its oldest pending event is discarded.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		for {
			select {
			case ch <- e:
			default:
				// Buffer full: drop the oldest and retry once; the loop
				// re-attempts in case another drain raced us.
				select {
				case <-ch:
				default:
				}
				continue
			}
			break
		}
	}
}

// Close shuts the bus down, closing all subscriber channels.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
