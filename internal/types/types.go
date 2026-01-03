package types

import "time"

// Task represents a background task that can be triggered
type Task struct {
	ID          string
	Name        string
	Description string
	LastRanAt   *time.Time
}
