package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/r-a-y-y-a/realtime-notifications-service/internal/config"
	"github.com/r-a-y-y-a/realtime-notifications-service/internal/infra"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rdb, err := infra.ConnectRedis(ctx, cfg.RedisAddr)
	if err != nil {
		slog.Error("failed to connect to Redis", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /ws", wsHandler(rdb))
	mux.HandleFunc("GET /sse", sseHandler(rdb))

	srv := &http.Server{
		Addr:        ":" + cfg.GatewayPort,
		Handler:     mux,
		ReadTimeout: 0, // long-lived connections
	}

	go func() {
		slog.Info("gateway server listening", "port", cfg.GatewayPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down gateway")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
}

func wsHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			http.Error(w, "missing user_id", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade error", "err", err)
			return
		}
		defer conn.Close()

		connCtx, connCancel := context.WithCancel(r.Context())
		defer connCancel()

		channel := "notifications:" + userID
		sub := rdb.Subscribe(connCtx, channel)
		defer sub.Close()

		// Goroutine to detect client disconnect
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					connCancel()
					return
				}
			}
		}()

		slog.Info("WebSocket connected", "user_id", userID)
		ch := sub.Channel()
		for {
			select {
			case <-connCtx.Done():
				slog.Info("WebSocket disconnected", "user_id", userID)
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload)); err != nil {
					slog.Error("websocket write error", "err", err)
					return
				}
			}
		}
	}
}

func sseHandler(rdb *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			http.Error(w, "missing user_id", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		connCtx, connCancel := context.WithCancel(r.Context())
		defer connCancel()

		channel := "notifications:" + userID
		sub := rdb.Subscribe(connCtx, channel)
		defer sub.Close()

		slog.Info("SSE connected", "user_id", userID)
		ch := sub.Channel()
		for {
			select {
			case <-connCtx.Done():
				slog.Info("SSE disconnected", "user_id", userID)
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg.Payload)
				flusher.Flush()
			}
		}
	}
}
