package main

import (
	"encoding/json"
	"sync"
)

// evt builds a JSON event payload with a "type" discriminator.
func evt(t string, fields map[string]any) []byte {
	m := make(map[string]any, len(fields)+1)
	m["type"] = t
	for k, v := range fields {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte(`{"type":"error"}`)
	}
	return b
}

type subscriber struct {
	key string // session code; "" subscribes to admin events
	ch  chan []byte
}

// broker fan-outs SSE events. Slow subscribers are dropped and rely on
// EventSource's automatic reconnect (they refetch full state on reconnect).
type broker struct {
	mu   sync.Mutex
	next int
	subs map[int]*subscriber
}

func newBroker() *broker {
	return &broker{subs: make(map[int]*subscriber)}
}

func (b *broker) subscribe(key string) (int, <-chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := b.next
	ch := make(chan []byte, 64)
	b.subs[id] = &subscriber{key: key, ch: ch}
	return id, ch
}

func (b *broker) unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(s.ch)
	}
}

func (b *broker) deliver(id int, s *subscriber, payload []byte) {
	select {
	case s.ch <- payload:
	default:
		delete(b.subs, id)
		close(s.ch)
	}
}

// publish sends to subscribers bound to exactly this session code.
func (b *broker) publish(key string, payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, s := range b.subs {
		if s.key == key {
			b.deliver(id, s, payload)
		}
	}
}

// publishAdmin sends to admin subscribers (empty key).
func (b *broker) publishAdmin(payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, s := range b.subs {
		if s.key == "" {
			b.deliver(id, s, payload)
		}
	}
}

// broadcast sends to every subscriber.
func (b *broker) broadcast(payload []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, s := range b.subs {
		b.deliver(id, s, payload)
	}
}

// count returns the number of subscribers bound to exactly this key.
func (b *broker) count(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, s := range b.subs {
		if s.key == key {
			n++
		}
	}
	return n
}
