package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"

	"github.com/r-a-y-y-a/realtime-notifications-service/internal/config"
	"github.com/r-a-y-y-a/realtime-notifications-service/internal/models"
)

var requestsTotal atomic.Int64

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Connect to Redis with retry
	rdb := connectRedis(ctx, cfg.RedisAddr)
	defer rdb.Close()

	// Connect to Postgres with retry
	pool := connectPostgres(ctx, cfg.PostgresDSN)
	defer pool.Close()

	// Create Kafka writer
	writer := &kafka.Writer{
		Addr:         kafka.TCP(strings.Split(cfg.KafkaBrokers, ",")...),
		Topic:        cfg.KafkaTopic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
	}
	defer writer.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP requests_total Total number of POST /notifications requests\n")
		fmt.Fprintf(w, "# TYPE requests_total counter\n")
		fmt.Fprintf(w, "requests_total %d\n", requestsTotal.Load())
	})

	mux.HandleFunc("POST /notifications", postNotificationHandler(cfg, rdb, writer))
	mux.HandleFunc("GET /users/{userID}/notifications", getUserNotificationsHandler(pool))

	srv := &http.Server{
		Addr:         ":" + cfg.APIPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("API server listening", "port", cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down API server")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
}

func postNotificationHandler(cfg *config.Config, rdb *redis.Client, writer *kafka.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestsTotal.Add(1)

		var req struct {
			UserID   string `json:"user_id"`
			TenantID string `json:"tenant_id"`
			Title    string `json:"title"`
			Body     string `json:"body"`
			Channel  string `json:"channel"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.UserID == "" || req.TenantID == "" || req.Title == "" || req.Body == "" || req.Channel == "" {
			jsonError(w, "missing required fields", http.StatusBadRequest)
			return
		}

		// Rate limiting: pipeline INCR + EXPIRE to avoid races
		rateKey := fmt.Sprintf("rate:%s:%s:minute", req.TenantID, req.UserID)
		pipe := rdb.Pipeline()
		incrCmd := pipe.Incr(r.Context(), rateKey)
		pipe.Expire(r.Context(), rateKey, 60*time.Second)
		if _, err := pipe.Exec(r.Context()); err != nil {
			slog.Error("redis pipeline error", "err", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		count := incrCmd.Val()
		if count > int64(cfg.RateLimitPerMinute) {
			jsonError(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		notifReq := models.NotificationRequest{
			RequestID: uuid.New().String(),
			UserID:    req.UserID,
			TenantID:  req.TenantID,
			Title:     req.Title,
			Body:      req.Body,
			Channel:   req.Channel,
		}

		payload, err := json.Marshal(notifReq)
		if err != nil {
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}

		msg := kafka.Message{
			Key:   []byte(req.UserID),
			Value: payload,
		}
		if err := writer.WriteMessages(r.Context(), msg); err != nil {
			slog.Error("kafka write error", "err", err)
			jsonError(w, "failed to publish notification", http.StatusInternalServerError)
			return
		}

		slog.Info("notification queued", "request_id", notifReq.RequestID, "user_id", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"request_id": notifReq.RequestID, "status": "queued"})
	}
}

func getUserNotificationsHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("userID")
		if userID == "" {
			jsonError(w, "missing user_id", http.StatusBadRequest)
			return
		}

		limit := 20
		offset := 0
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				if n > 100 {
					n = 100
				}
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		rows, err := pool.Query(r.Context(),
			`SELECT id, user_id, tenant_id, title, body, channel, status, created_at
			 FROM notifications WHERE user_id = $1
			 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
			userID, limit, offset)
		if err != nil {
			slog.Error("postgres query error", "err", err)
			jsonError(w, "internal server error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		notifications := []models.Notification{}
		for rows.Next() {
			var n models.Notification
			if err := rows.Scan(&n.ID, &n.UserID, &n.TenantID, &n.Title, &n.Body, &n.Channel, &n.Status, &n.CreatedAt); err != nil {
				slog.Error("row scan error", "err", err)
				jsonError(w, "internal server error", http.StatusInternalServerError)
				return
			}
			notifications = append(notifications, n)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(notifications)
	}
}

func connectRedis(ctx context.Context, addr string) *redis.Client {
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	backoff := time.Second
	for {
		if err := rdb.Ping(ctx).Err(); err == nil {
			slog.Info("connected to Redis", "addr", addr)
			return rdb
		}
		slog.Warn("waiting for Redis", "addr", addr, "retry_in", backoff)
		select {
		case <-ctx.Done():
			os.Exit(1)
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func connectPostgres(ctx context.Context, dsn string) *pgxpool.Pool {
	backoff := time.Second
	for {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				slog.Info("connected to Postgres")
				return pool
			}
			pool.Close()
		}
		slog.Warn("waiting for Postgres", "retry_in", backoff, "err", err)
		select {
		case <-ctx.Done():
			os.Exit(1)
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
