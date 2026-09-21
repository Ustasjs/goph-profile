package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// Publisher sends avatar events. It holds one connection and one
// channel; a lost connection surfaces as publish errors, which the
// caller treats as non-fatal (the MVP has no reconnect).
type Publisher struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher dials the broker and declares the topology.
func NewPublisher(url string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	if err := Declare(ch); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Publisher{conn: conn, ch: ch}, nil
}

// PublishUpload emits the thumbnail job for one avatar.
func (p *Publisher) PublishUpload(ctx context.Context, ev avatar.UploadEvent) error {
	return p.publish(ctx, QueueUpload, ev.AvatarID, ev)
}

// PublishDelete emits the S3 cleanup job for one avatar.
func (p *Publisher) PublishDelete(ctx context.Context, ev avatar.DeleteEvent) error {
	return p.publish(ctx, QueueDelete, ev.AvatarID, ev)
}

func (p *Publisher) publish(ctx context.Context, key, messageID string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	err = p.ch.PublishWithContext(ctx, Exchange, key, false, false, amqp.Publishing{
		ContentType: "application/json",
		// The avatar id doubles as the message id, so consumers can
		// deduplicate deliveries.
		MessageId:    messageID,
		DeliveryMode: amqp.Persistent,
		Body:         data,
	})
	if err != nil {
		return fmt.Errorf("publish %s: %w", key, err)
	}
	return nil
}

// Ping reports whether the connection is still alive. Used by /health.
func (p *Publisher) Ping(context.Context) error {
	if p.conn.IsClosed() {
		return errors.New("rabbitmq connection is closed")
	}
	return nil
}

// Close shuts the connection down.
func (p *Publisher) Close() error {
	return p.conn.Close()
}
