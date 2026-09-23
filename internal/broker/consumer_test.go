package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// fakeAck records acknowledgements without a broker: amqp.Delivery
// calls back into its Acknowledger interface.
type fakeAck struct {
	acks  int
	nacks []bool // requeue flag per nack
}

func (a *fakeAck) Ack(uint64, bool) error { a.acks++; return nil }

func (a *fakeAck) Nack(_ uint64, _, requeue bool) error {
	a.nacks = append(a.nacks, requeue)
	return nil
}

func (a *fakeAck) Reject(uint64, bool) error { return nil }

func newUnitConsumer() *Consumer {
	return &Consumer{backoff: []time.Duration{0}, log: zap.NewNop()}
}

func delivery(ack *fakeAck) amqp.Delivery {
	return amqp.Delivery{Acknowledger: ack, Body: []byte(`{}`)}
}

func TestNewConsumerRejectsBadArguments(t *testing.T) {
	// Both checks fire before any dial, so no broker is needed.
	_, err := NewConsumer("", 1, zap.NewNop())
	assert.Error(t, err)

	_, err = NewConsumer("amqp://guest:guest@localhost:5672/", 0, zap.NewNop())
	assert.Error(t, err)
}

func TestNewPublisherRequiresURL(t *testing.T) {
	_, err := NewPublisher("")
	assert.Error(t, err)
}

func TestProcessAcksOnSuccess(t *testing.T) {
	c := newUnitConsumer()
	ack := &fakeAck{}

	c.process(context.Background(), delivery(ack),
		func(context.Context, amqp.Delivery) error { return nil }, nil)

	assert.Equal(t, 1, ack.acks)
	assert.Empty(t, ack.nacks)
}

func TestProcessBuriesAfterRetries(t *testing.T) {
	c := newUnitConsumer()
	ack := &fakeAck{}
	deadCalled := false

	c.process(context.Background(), delivery(ack),
		func(context.Context, amqp.Delivery) error { return errors.New("always failing") },
		func(context.Context, amqp.Delivery) { deadCalled = true })

	assert.Zero(t, ack.acks)
	// requeue=false: the message must go to the dead-letter exchange.
	assert.Equal(t, []bool{false}, ack.nacks)
	assert.True(t, deadCalled)
}

func TestProcessRequeuesOnShutdown(t *testing.T) {
	c := newUnitConsumer()
	ack := &fakeAck{}
	ctx, cancel := context.WithCancel(context.Background())

	c.process(ctx, delivery(ack),
		func(context.Context, amqp.Delivery) error {
			cancel() // shutdown arrives mid-processing
			return errors.New("interrupted")
		},
		func(context.Context, amqp.Delivery) { t.Fatal("dead callback must not fire on shutdown") })

	// requeue=true: the message goes back for the next worker.
	assert.Equal(t, []bool{true}, ack.nacks)
}

func TestLoop(t *testing.T) {
	t.Run("handles then stops on closed channel", func(t *testing.T) {
		c := newUnitConsumer()
		ack := &fakeAck{}
		msgs := make(chan amqp.Delivery, 1)
		msgs <- delivery(ack)
		close(msgs)

		err := c.loop(context.Background(), msgs,
			func(context.Context, amqp.Delivery) error { return nil }, nil)

		// A closed channel with a live context is a broken connection.
		require.Error(t, err)
		assert.Equal(t, 1, ack.acks)
	})

	t.Run("closed channel on shutdown is clean", func(t *testing.T) {
		c := newUnitConsumer()
		msgs := make(chan amqp.Delivery)
		close(msgs)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Both select branches are ready; either outcome reads as a
		// clean shutdown for Run (which maps context.Canceled to nil).
		err := c.loop(ctx, msgs,
			func(context.Context, amqp.Delivery) error { return nil }, nil)
		if err != nil {
			assert.ErrorIs(t, err, context.Canceled)
		}
	})

	t.Run("returns on context cancel", func(t *testing.T) {
		c := newUnitConsumer()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := c.loop(ctx, make(chan amqp.Delivery),
			func(context.Context, amqp.Delivery) error { return nil }, nil)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
