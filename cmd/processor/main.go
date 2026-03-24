package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"

	"github.com/r-a-y-y-a/realtime-notifications-service/internal/config"
	"github.com/r-a-y-y-a/realtime-notifications-service/internal/infra"
	"github.com/r-a-y-y-a/realtime-notifications-service/internal/models"
)

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

	pool, err := infra.ConnectPostgres(ctx, cfg.PostgresDSN)
	if err != nil {
		slog.Error("failed to connect to Postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: strings.Split(cfg.KafkaBrokers, ","),
		Topic:   cfg.KafkaTopic,
		GroupID: "notification-processor",
	})
	defer reader.Close()

	dlqWriter := &kafka.Writer{
		Addr:         kafka.TCP(strings.Split(cfg.KafkaBrokers, ",")...),
		Topic:        cfg.KafkaDLQTopic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
	}
	defer dlqWriter.Close()

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
			slog.Error("processing failed, sending to DLQ",
				"err", err, "offset", msg.Offset, "topic", cfg.KafkaDLQTopic)

			// Send to DLQ for manual inspection/retry
			dlqMsg := kafka.Message{
				Key:   msg.Key,
				Value: msg.Value,
				Headers: []kafka.Header{
					{Key: "error", Value: []byte(err.Error())},
					{Key: "original_offset", Value: []byte(strconv.FormatInt(msg.Offset, 10))},
				},
			}
			if dlqErr := dlqWriter.WriteMessages(ctx, dlqMsg); dlqErr != nil {
				slog.Error("DLQ write failed — message will be lost",
					"dlq_err", dlqErr, "original_offset", msg.Offset)
			}
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

	// Idempotency and notification persistence in a single Postgres transaction
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Record that we've processed this request_id (duplicate check via unique constraint)
	tag, err := tx.Exec(ctx,
		`INSERT INTO processed_requests (request_id) VALUES ($1) ON CONFLICT DO NOTHING`,
		req.RequestID,
	)
	if err != nil {
		return err
	}

	// If 0 rows affected, this message was already processed
	if tag.RowsAffected() == 0 {
		slog.Info("duplicate message skipped", "request_id", req.RequestID)
		return tx.Rollback(ctx)
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

	// Insert notification in same transaction
	_, err = tx.Exec(ctx,
		`INSERT INTO notifications (id, user_id, tenant_id, title, body, channel, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		notif.ID, notif.UserID, notif.TenantID, notif.Title, notif.Body, notif.Channel, notif.Status, notif.CreatedAt,
	)
	if err != nil {
		return err
	}

	// Commit transaction (atomic: both inserts succeed or both fail)
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Post-commit Redis updates (best-effort, non-critical)
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
