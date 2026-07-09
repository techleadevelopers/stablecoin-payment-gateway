package workers

import (
	"sync"
	"sync/atomic"
)

// Event is the internal worker bus message.
type Event struct {
	Type    string
	OrderID string
	Payload map[string]interface{}
}

// EventBus distributes events to worker subscribers.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]chan Event
	closed      bool
	published   atomic.Uint64
	dropped     atomic.Uint64
}

type EventBusMetrics struct {
	Subscribers int    `json:"subscribers"`
	QueueSize   int    `json:"queueSize"`
	Published   uint64 `json:"published"`
	Dropped     uint64 `json:"dropped"`
	Closed      bool   `json:"closed"`
}

func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[string][]chan Event),
	}
}

func (b *EventBus) Subscribe(eventType string) chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, 100)
	if b.closed {
		close(ch)
		return ch
	}
	b.subscribers[eventType] = append(b.subscribers[eventType], ch)
	return ch
}

func (b *EventBus) Unsubscribe(eventType string, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[eventType]
	for i, sub := range subs {
		if sub == ch {
			b.subscribers[eventType] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}
	if len(b.subscribers[eventType]) == 0 {
		delete(b.subscribers, eventType)
	}
}

func (b *EventBus) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		b.dropped.Add(1)
		return
	}
	b.published.Add(1)

	for _, ch := range b.subscribers[event.Type] {
		select {
		case ch <- event:
		default:
			b.dropped.Add(1)
		}
	}
}

func (b *EventBus) Metrics() EventBusMetrics {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var subscribers int
	var queueSize int
	for _, subs := range b.subscribers {
		subscribers += len(subs)
		for _, ch := range subs {
			queueSize += len(ch)
		}
	}

	return EventBusMetrics{
		Subscribers: subscribers,
		QueueSize:   queueSize,
		Published:   b.published.Load(),
		Dropped:     b.dropped.Load(),
		Closed:      b.closed,
	}
}

func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for eventType, subs := range b.subscribers {
		for _, ch := range subs {
			close(ch)
		}
		delete(b.subscribers, eventType)
	}
}

func (b *EventBus) Shutdown() {
	b.Close()
}
