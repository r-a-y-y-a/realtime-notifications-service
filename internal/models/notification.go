package models

import "time"

// NotificationRequest is the event published to Kafka
type NotificationRequest struct {
	RequestID string `json:"request_id"` // idempotency key
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Channel   string `json:"channel"` // "push", "email", "sms"
}

// Notification is the persisted record in Postgres
type Notification struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	TenantID  string    `json:"tenant_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Channel   string    `json:"channel"`
	Status    string    `json:"status"` // "unread", "read", "delivered"
	CreatedAt time.Time `json:"created_at"`
}
