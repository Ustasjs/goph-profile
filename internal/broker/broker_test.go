package broker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ustasjs/goph-profile/internal/avatar"
)

func TestEventJSON(t *testing.T) {
	up := avatar.UploadEvent{AvatarID: "a1", UserID: "u1", S3Key: "avatars/a1/original"}
	data, err := json.Marshal(up)
	require.NoError(t, err)
	assert.JSONEq(t, `{"avatar_id":"a1","user_id":"u1","s3_key":"avatars/a1/original"}`, string(data))

	del := avatar.DeleteEvent{AvatarID: "a1", S3Keys: []string{"k1", "k2"}}
	data, err = json.Marshal(del)
	require.NoError(t, err)
	assert.JSONEq(t, `{"avatar_id":"a1","s3_keys":["k1","k2"]}`, string(data))
}

func TestWithRetry(t *testing.T) {
	c := &Consumer{backoff: []time.Duration{0, 0, 0}, log: slog.New(slog.DiscardHandler)}

	t.Run("succeeds after failures", func(t *testing.T) {
		attempts := 0
		err := c.withRetry(context.Background(), amqp.Delivery{}, func(context.Context, amqp.Delivery) error {
			attempts++
			if attempts < 3 {
				return errors.New("transient")
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 3, attempts)
	})

	t.Run("gives up after all retries", func(t *testing.T) {
		attempts := 0
		err := c.withRetry(context.Background(), amqp.Delivery{}, func(context.Context, amqp.Delivery) error {
			attempts++
			return errors.New("permanent")
		})
		require.Error(t, err)
		// One initial attempt plus one retry per backoff entry.
		assert.Equal(t, 4, attempts)
	})

	t.Run("stops on canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		err := c.withRetry(ctx, amqp.Delivery{}, func(context.Context, amqp.Delivery) error {
			attempts++
			cancel()
			return errors.New("failing")
		})
		require.Error(t, err)
		assert.Equal(t, 1, attempts)
	})
}

// newBrokerPair connects a publisher and a consumer to the broker
// from RABBITMQ_URL. Without the variable the test is skipped, so
// plain "go test" works with no RabbitMQ around.
func newBrokerPair(t *testing.T) (*Publisher, *Consumer) {
	t.Helper()

	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		t.Skip("RABBITMQ_URL is not set")
	}

	pub, err := NewPublisher(url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close() })

	cons, err := NewConsumer(url, 1, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	cons.SetBackoff([]time.Duration{10 * time.Millisecond})
	t.Cleanup(func() { _ = cons.Close() })

	return pub, cons
}

func TestPublishConsume(t *testing.T) {
	pub, cons := newBrokerPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	upEv := avatar.UploadEvent{AvatarID: uuid.NewString(), UserID: "u1", S3Key: "k"}
	delEv := avatar.DeleteEvent{AvatarID: uuid.NewString(), S3Keys: []string{"k1"}}

	gotUpload := make(chan avatar.UploadEvent, 10)
	gotDelete := make(chan avatar.DeleteEvent, 10)
	done := make(chan error, 1)
	go func() {
		done <- cons.Run(ctx, Handlers{
			OnUpload: func(_ context.Context, ev avatar.UploadEvent) error {
				gotUpload <- ev
				return nil
			},
			OnDelete: func(_ context.Context, ev avatar.DeleteEvent) error {
				gotDelete <- ev
				return nil
			},
		})
	}()

	require.NoError(t, pub.PublishUpload(ctx, upEv))
	require.NoError(t, pub.PublishDelete(ctx, delEv))

	waitEvent(ctx, t, gotUpload, func(got avatar.UploadEvent) bool { return got == upEv })
	waitEvent(ctx, t, gotDelete, func(got avatar.DeleteEvent) bool { return got.AvatarID == delEv.AvatarID })

	cancel()
	require.NoError(t, <-done)
}

func TestDeadLetterAfterRetries(t *testing.T) {
	pub, cons := newBrokerPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ev := avatar.UploadEvent{AvatarID: uuid.NewString(), UserID: "u1", S3Key: "k"}

	deadCalled := make(chan avatar.UploadEvent, 1)
	attempts := 0
	done := make(chan error, 1)
	go func() {
		done <- cons.Run(ctx, Handlers{
			OnUpload: func(_ context.Context, got avatar.UploadEvent) error {
				if got.AvatarID != ev.AvatarID {
					// A leftover from another test run: ack and move on.
					return nil
				}
				attempts++
				return errors.New("always failing")
			},
			OnDelete:     func(context.Context, avatar.DeleteEvent) error { return nil },
			OnUploadDead: func(_ context.Context, got avatar.UploadEvent) { deadCalled <- got },
		})
	}()

	require.NoError(t, pub.PublishUpload(ctx, ev))

	waitEvent(ctx, t, deadCalled, func(got avatar.UploadEvent) bool { return got == ev })
	assert.Equal(t, 2, attempts) // initial attempt + one retry (test backoff has one entry)

	// The message must have landed in the dead queue.
	require.Eventually(t, func() bool {
		d, ok, err := pub.ch.Get(QueueDead, true)
		if err != nil || !ok {
			return false
		}
		var got avatar.UploadEvent
		if err := json.Unmarshal(d.Body, &got); err != nil {
			return false
		}
		return got.AvatarID == ev.AvatarID
	}, 5*time.Second, 100*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

func waitEvent[T any](ctx context.Context, t *testing.T, ch <-chan T, match func(T) bool) {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for the event")
		case got := <-ch:
			// Other tests may leave events in the durable queues;
			// skip everything that is not ours.
			if match(got) {
				return
			}
		}
	}
}
