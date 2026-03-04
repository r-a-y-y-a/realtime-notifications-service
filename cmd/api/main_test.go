package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestPostNotification_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"missing user_id", `{"tenant_id":"t1","title":"T","body":"B","channel":"push"}`},
		{"missing tenant_id", `{"user_id":"u1","title":"T","body":"B","channel":"push"}`},
		{"missing title", `{"user_id":"u1","tenant_id":"t1","body":"B","channel":"push"}`},
		{"missing body", `{"user_id":"u1","tenant_id":"t1","title":"T","channel":"push"}`},
		{"missing channel", `{"user_id":"u1","tenant_id":"t1","title":"T","body":"B"}`},
		{"empty body", `{}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/notifications",
				bytes.NewBufferString(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// Call the validation logic directly via the handler with nil dependencies.
			// The handler returns 400 before touching Redis/Kafka for missing fields.
			handler := postNotificationHandler(nil, nil, nil)
			handler(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d (payload: %s)", rec.Code, tc.payload)
			}
			var errResp map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if errResp["error"] == "" {
				t.Error("expected non-empty error field")
			}
		})
	}
}

func TestPostNotification_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/notifications",
		bytes.NewBufferString("not-json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler := postNotificationHandler(nil, nil, nil)
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestGetUserNotifications_MissingUserID(t *testing.T) {
	// With a nil pool this would panic if it reaches the DB call,
	// but we expect a 400 because userID extracted from path will be "".
	// We simulate by calling with a path that gives no PathValue.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{userID}/notifications", getUserNotificationsHandler(nil))

	// Request with empty string userID shouldn't happen via normal routing,
	// but test the handler in isolation with an empty path value.
	req := httptest.NewRequest(http.MethodGet, "/users//notifications", nil)
	rec := httptest.NewRecorder()

	// The handler checks userID == "" and returns 400; the mux won't even
	// route to it for an empty segment, so we call the handler directly.
	getUserNotificationsHandler(nil)(rec, req)
	// r.PathValue returns "" when not using a proper mux; handler returns 400.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty userID, got %d", rec.Code)
	}
}

func TestJsonError(t *testing.T) {
	rec := httptest.NewRecorder()
	jsonError(rec, "something went wrong", http.StatusUnprocessableEntity)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json Content-Type, got %q", ct)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body["error"] != "something went wrong" {
		t.Errorf("got error %q", body["error"])
	}
}
