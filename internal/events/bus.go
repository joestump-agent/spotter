package events

import (
	"sync"
)

type EventType string

const (
	EventTypeRecentListen EventType = "recent-listen"
	EventTypeNotification EventType = "notification"
)

type Event struct {
	Type    EventType
	Payload any
}

type NotificationPayload struct {
	Title    string
	Message  string
	IconType string
}

type Bus struct {
	mu          sync.RWMutex
	subscribers map[int][]chan Event
}

func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[int][]chan Event),
	}
}

// Subscribe returns a channel that receives events for the given user,
// and a cleanup function that must be called when the subscription is no longer needed.
func (b *Bus) Subscribe(userID int) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, 10)
	b.subscribers[userID] = append(b.subscribers[userID], ch)

	cleanup := func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		chans := b.subscribers[userID]
		for i, c := range chans {
			if c == ch {
				// Remove from slice
				b.subscribers[userID] = append(chans[:i], chans[i+1:]...)
				close(ch)
				break
			}
		}
		if len(b.subscribers[userID]) == 0 {
			delete(b.subscribers, userID)
		}
	}

	return ch, cleanup
}

func (b *Bus) Publish(userID int, event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if chans, ok := b.subscribers[userID]; ok {
		for _, ch := range chans {
			// Non-blocking send to prevent blocking the publisher if a client is slow
			select {
			case ch <- event:
			default:
				// Channel full, drop event
			}
		}
	}
}
