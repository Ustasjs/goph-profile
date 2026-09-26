package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

// defaultBackoff is the wait before each retry: one initial attempt
// plus one retry per entry, then the message goes to the dead queue.
var defaultBackoff = []time.Duration{time.Second, 4 * time.Second, 16 * time.Second}

// Handlers react to consumed events. A handler error triggers the
// retry cycle; nil acknowledges the message.
type Handlers struct {
	OnUpload func(ctx context.Context, ev avatar.UploadEvent) error
	OnDelete func(ctx context.Context, ev avatar.DeleteEvent) error
	// OnUploadDead fires once when OnUpload exhausted its retries,
	// right before the message moves to the dead queue. Optional.
	OnUploadDead func(ctx context.Context, ev avatar.UploadEvent)
}

// Consumer reads both work queues and drives the retry cycle.
type Consumer struct {
	conn    *amqp.Connection
	ch      *amqp.Channel
	backoff []time.Duration
	log     *slog.Logger
}

// NewConsumer dials the broker, declares the topology and caps the
// in-flight deliveries at prefetch.
func NewConsumer(url string, prefetch int, log *slog.Logger) (*Consumer, error) {
	if url == "" {
		return nil, errors.New("broker: url is required")
	}
	if prefetch < 1 {
		return nil, errors.New("broker: prefetch must be at least 1")
	}

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
	if err := ch.Qos(prefetch, 0, false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}
	return &Consumer{conn: conn, ch: ch, backoff: defaultBackoff, log: log}, nil
}

// SetBackoff overrides the retry schedule. Tests use it to avoid
// real waits.
func (c *Consumer) SetBackoff(backoff []time.Duration) {
	c.backoff = backoff
}

// Close shuts the connection down. Run returns soon after.
func (c *Consumer) Close() error {
	return c.conn.Close()
}

// Run consumes both queues until the context is canceled or the
// connection dies.
func (c *Consumer) Run(ctx context.Context, h Handlers) error {
	uploads, err := c.ch.Consume(QueueUpload, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", QueueUpload, err)
	}
	deletes, err := c.ch.Consume(QueueDelete, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", QueueDelete, err)
	}

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return c.loop(gCtx, uploads, func(ctx context.Context, d amqp.Delivery) error {
			var ev avatar.UploadEvent
			if err := json.Unmarshal(d.Body, &ev); err != nil {
				return fmt.Errorf("decode upload event: %w", err)
			}
			return h.OnUpload(ctx, ev)
		}, func(ctx context.Context, d amqp.Delivery) {
			if h.OnUploadDead == nil {
				return
			}
			var ev avatar.UploadEvent
			if err := json.Unmarshal(d.Body, &ev); err != nil {
				return
			}
			h.OnUploadDead(ctx, ev)
		})
	})

	g.Go(func() error {
		return c.loop(gCtx, deletes, func(ctx context.Context, d amqp.Delivery) error {
			var ev avatar.DeleteEvent
			if err := json.Unmarshal(d.Body, &ev); err != nil {
				return fmt.Errorf("decode delete event: %w", err)
			}
			return h.OnDelete(ctx, ev)
		}, nil)
	})

	// Closing the connection ends both Consume streams, which lets
	// the loops drain and return.
	g.Go(func() error {
		<-gCtx.Done()
		return c.conn.Close()
	})

	err = g.Wait()
	if errors.Is(err, context.Canceled) || errors.Is(err, amqp.ErrClosed) {
		return nil
	}
	return err
}

// loop processes one queue sequentially: attempt, retry with
// backoff, and on final failure hand the message to the dead queue.
func (c *Consumer) loop(ctx context.Context, msgs <-chan amqp.Delivery,
	handle func(context.Context, amqp.Delivery) error,
	dead func(context.Context, amqp.Delivery)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-msgs:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("delivery channel closed")
			}
			c.process(ctx, d, handle, dead)
		}
	}
}

func (c *Consumer) process(ctx context.Context, d amqp.Delivery,
	handle func(context.Context, amqp.Delivery) error,
	dead func(context.Context, amqp.Delivery)) {
	// Continue the trace the publisher started: one span covers the
	// delivery including all retry attempts.
	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier(d.Headers))
	ctx, span := tracer.Start(ctx, "consume "+d.RoutingKey,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "rabbitmq"),
			attribute.String("messaging.destination.name", d.RoutingKey),
			attribute.String("messaging.message.id", d.MessageId),
		))
	defer span.End()

	err := c.withRetry(ctx, d, handle)
	if err == nil {
		if ackErr := d.Ack(false); ackErr != nil {
			c.log.ErrorContext(ctx, "ack", "error", ackErr)
		}
		return
	}

	// A canceled context means shutdown, not a bad message: requeue
	// it for the next worker instead of burying it.
	if ctx.Err() != nil {
		_ = d.Nack(false, true)
		return
	}

	span.RecordError(err)
	span.SetStatus(codes.Error, "exhausted retries")
	c.log.ErrorContext(ctx, "message exhausted retries, moving to dead queue",
		"routing_key", d.RoutingKey,
		"message_id", d.MessageId,
		"error", err)
	if dead != nil {
		dead(ctx, d)
	}
	// requeue=false sends the message to the dead-letter exchange.
	if nackErr := d.Nack(false, false); nackErr != nil {
		c.log.ErrorContext(ctx, "nack", "error", nackErr)
	}
}

// withRetry runs handle once plus one retry per backoff entry.
func (c *Consumer) withRetry(ctx context.Context, d amqp.Delivery,
	handle func(context.Context, amqp.Delivery) error) error {
	var err error
	for attempt := 0; ; attempt++ {
		err = handle(ctx, d)
		if err == nil || attempt >= len(c.backoff) || ctx.Err() != nil {
			return err
		}
		c.log.WarnContext(ctx, "handler failed, retrying",
			"message_id", d.MessageId,
			"attempt", attempt+1,
			"wait", c.backoff[attempt],
			"error", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(c.backoff[attempt]):
		}
	}
}
