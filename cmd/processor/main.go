package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"

	"github.com/r-a-y-y-a/realtime-notifications-service/internal/config"
	"github.com/r-a-y-y-a/realtime-notifications-service/internal/models"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	rdb := connectRedis(ctx, cfg.RedisAddr)
	defer rdb.Close()

	pool := connectPostgres(ctx, cfg.PostgresDSN)
	defer pool.Close()

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: strings.Split(cfg.KafkaBrokers, ","),
		Topic:   cfg.KafkaTopic,
		GroupID: "notification-processor",
	})
	defer reader.Close()

	slog.Info("processor started, consuming messages")

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				slog.Info("processor shutting down")
				return
			}
			slog.Error("kafka fetch error", "err", err)
			continue
		}

		if err := processMessage(ctx, msg, rdb, pool); err != nil {
			slog.Error("processing failed", "err", err, "offset", msg.Offset)
			// Still commit to avoid infinite retry loops; a DLQ would be used in production
		}

		if err := reader.CommitMessages(ctx, msg); err != nil {
			slog.Error("kafka commit error", "err", err)
		}
	}
}

func processMessage(ctx context.Context, msg kafka.Message, rdb *redis.Client, pool *pgxpool.Pool) error {
	var req models.NotificationRequest
	if err := json.Unmarshal(msg.Value, &req); err != nil {
		slog.Error("unmarshal error", "err", err)
		return nil // malformed message; skip
	}

	// Idempotency check via Redis SETNX
	idempotencyKey := "processed:" + req.RequestID
	set, err := rdb.SetNX(ctx, idempotencyKey, 1, 24*time.Hour).Result()
	if err != nil {
		return err
	}
	if !set {
		slog.Info("duplicate message skipped", "request_id", req.RequestID)
		return nil
	}

	notif := models.Notification{
		ID:        uuid.New().String(),
		UserID:    req.UserID,
		TenantID:  req.TenantID,
		Title:     req.Title,
		Body:      req.Body,
		Channel:   req.Channel,
		Status:    "unread",
		CreatedAt: time.Now().UTC(),
	}

	// Insert into Postgres
	_, err = pool.Exec(ctx,
		`INSERT INTO notifications (id, user_id, tenant_id, title, body, channel, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		notif.ID, notif.UserID, notif.TenantID, notif.Title, notif.Body, notif.Channel, notif.Status, notif.CreatedAt,
	)
	if err != nil {
		// Roll back idempotency key so we can retry
		rdb.Del(ctx, idempotencyKey)
		return err
	}

	// Update Redis unread count and list; log but don't fail on Redis errors
	if err := rdb.Incr(ctx, "user:"+notif.UserID+":unread_count").Err(); err != nil {
		slog.Error("redis incr unread_count error", "err", err, "user_id", notif.UserID)
	}
	if err := rdb.RPush(ctx, "user:"+notif.UserID+":unread_ids", notif.ID).Err(); err != nil {
		slog.Error("redis rpush unread_ids error", "err", err, "user_id", notif.UserID)
	}

	// Publish to Redis Pub/Sub for delivery gateway
	payload, err := json.Marshal(notif)
	if err != nil {
		return err
	}
	if err := rdb.Publish(ctx, "notifications:"+notif.UserID, payload).Err(); err != nil {
		slog.Error("redis publish error", "err", err)
	}

	slog.Info("notification processed", "id", notif.ID, "user_id", notif.UserID)
	return nil
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
