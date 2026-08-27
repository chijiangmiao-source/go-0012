package audit

import (
	"sync"
	"time"

	"offshore-buoy-drift-search-loop/internal/domain"
)

type Notification struct {
	ID            int64       `json:"id"`
	TaskID        int64       `json:"task_id"`
	RecipientRole domain.Role `json:"recipient_role,omitempty"`
	RecipientID   string      `json:"recipient_id,omitempty"`
	Type          string      `json:"type"`
	DedupeKey     string      `json:"dedupe_key"`
	Title         string      `json:"title"`
	CreatedAt     time.Time   `json:"created_at"`
	ReadAt        *time.Time  `json:"read_at,omitempty"`
	ResolvedAt    *time.Time  `json:"resolved_at,omitempty"`
}

type NotificationCenter struct {
	mu            sync.Mutex
	nextID        int64
	notifications []Notification
}

func NewNotificationCenter() *NotificationCenter {
	return &NotificationCenter{nextID: 1}
}

func (c *NotificationCenter) Notify(input Notification) Notification {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, existing := range c.notifications {
		if existing.DedupeKey == input.DedupeKey && existing.ResolvedAt == nil {
			return existing
		}
	}
	input.ID = c.nextID
	c.nextID++
	input.CreatedAt = domain.UTC(input.CreatedAt)
	c.notifications = append(c.notifications, input)
	return input
}

func (c *NotificationCenter) List() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Notification, len(c.notifications))
	copy(out, c.notifications)
	return out
}

func (c *NotificationCenter) UnresolvedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	for _, n := range c.notifications {
		if n.ResolvedAt == nil {
			count++
		}
	}
	return count
}
