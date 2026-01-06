package events

import (
	"sync"
)

type EventType string

const (
	EventTypeRecentListen EventType = "recent-listen"
	EventTypeNotification EventType = "notification"

	// Vibes/Mixtape events
	EventTypeMixtapeGenerating EventType = "mixtape-generating"
	EventTypeMixtapeGenerated  EventType = "mixtape-generated"
	EventTypeMixtapeError      EventType = "mixtape-error"
)

type Event struct {
	Type    EventType
	Payload any
}

type NotificationPayload struct {
	Title    string
	Message  string
	IconType string // "success", "error", "warning", "info"
}

// MixtapeGeneratingPayload is sent when mixtape generation starts.
type MixtapeGeneratingPayload struct {
	MixtapeID   int
	MixtapeName string
	DJName      string
}

// MixtapeGeneratedPayload is sent when mixtape generation completes successfully.
type MixtapeGeneratedPayload struct {
	MixtapeID    int
	MixtapeName  string
	DJName       string
	TracksCount  int
	MatchedCount int
	TokensUsed   int
}

// MixtapeErrorPayload is sent when mixtape generation fails.
type MixtapeErrorPayload struct {
	MixtapeID   int
	MixtapeName string
	Error       string
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

// PublishNotification is a convenience method to publish a notification event.
func (b *Bus) PublishNotification(userID int, title, message, iconType string) {
	b.Publish(userID, Event{
		Type: EventTypeNotification,
		Payload: NotificationPayload{
			Title:    title,
			Message:  message,
			IconType: iconType,
		},
	})
}

// PublishMixtapeGenerating publishes an event when mixtape generation starts.
func (b *Bus) PublishMixtapeGenerating(userID int, mixtapeID int, mixtapeName, djName string) {
	b.Publish(userID, Event{
		Type: EventTypeMixtapeGenerating,
		Payload: MixtapeGeneratingPayload{
			MixtapeID:   mixtapeID,
			MixtapeName: mixtapeName,
			DJName:      djName,
		},
	})
}

// PublishMixtapeGenerated publishes an event when mixtape generation completes.
func (b *Bus) PublishMixtapeGenerated(userID int, mixtapeID int, mixtapeName, djName string, tracksCount, matchedCount, tokensUsed int) {
	b.Publish(userID, Event{
		Type: EventTypeMixtapeGenerated,
		Payload: MixtapeGeneratedPayload{
			MixtapeID:    mixtapeID,
			MixtapeName:  mixtapeName,
			DJName:       djName,
			TracksCount:  tracksCount,
			MatchedCount: matchedCount,
			TokensUsed:   tokensUsed,
		},
	})
}

// PublishMixtapeError publishes an event when mixtape generation fails.
func (b *Bus) PublishMixtapeError(userID int, mixtapeID int, mixtapeName, errorMsg string) {
	b.Publish(userID, Event{
		Type: EventTypeMixtapeError,
		Payload: MixtapeErrorPayload{
			MixtapeID:   mixtapeID,
			MixtapeName: mixtapeName,
			Error:       errorMsg,
		},
	})
}
