package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNotification_JSONRoundtrip(t *testing.T) {
	now := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	n := Notification{
		ID:        "abc-123",
		UserID:    "user-1",
		TenantID:  "tenant-1",
		Title:     "Hello",
		Body:      "World",
		Channel:   "push",
		Status:    "unread",
		CreatedAt: now,
	}

	data, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded Notification
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.ID != n.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, n.ID)
	}
	if decoded.UserID != n.UserID {
		t.Errorf("UserID mismatch")
	}
	if decoded.Status != n.Status {
		t.Errorf("Status mismatch")
	}
	if !decoded.CreatedAt.Equal(n.CreatedAt) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", decoded.CreatedAt, n.CreatedAt)
	}
}

func TestNotificationRequest_JSONRoundtrip(t *testing.T) {
	req := NotificationRequest{
		RequestID: "req-456",
		UserID:    "user-2",
		TenantID:  "tenant-2",
		Title:     "Test",
		Body:      "Test body",
		Channel:   "email",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var decoded NotificationRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if decoded.RequestID != req.RequestID {
		t.Errorf("RequestID mismatch: got %q, want %q", decoded.RequestID, req.RequestID)
	}
	if decoded.Channel != req.Channel {
		t.Errorf("Channel mismatch: got %q, want %q", decoded.Channel, req.Channel)
	}
}
