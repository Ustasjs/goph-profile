// Package broker moves avatar events through RabbitMQ: a direct
// exchange with one queue per event kind and a dead-letter queue
// for messages that exhausted their retries.
package broker

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Topology names. Routing keys equal queue names: the exchange is
// direct, one kind of event per queue.
const (
	Exchange    = "avatars"
	DLXExchange = "avatars.dlx"
	QueueUpload = "avatar.upload"
	QueueDelete = "avatar.delete"
	QueueDead   = "avatar.dead"
)

// Declare creates the exchanges, queues and bindings. Every declare
// is idempotent, so both the publisher and the consumer call it at
// startup and either binary can start first.
func Declare(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(Exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange: %w", err)
	}
	if err := ch.ExchangeDeclare(DLXExchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dlx exchange: %w", err)
	}

	// Work queues dead-letter into the DLX; a rejected message keeps
	// its routing key, so the dead queue binds to both keys.
	workArgs := amqp.Table{"x-dead-letter-exchange": DLXExchange}
	for _, queue := range []string{QueueUpload, QueueDelete} {
		if _, err := ch.QueueDeclare(queue, true, false, false, false, workArgs); err != nil {
			return fmt.Errorf("declare queue %s: %w", queue, err)
		}
		if err := ch.QueueBind(queue, queue, Exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", queue, err)
		}
	}

	if _, err := ch.QueueDeclare(QueueDead, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dead queue: %w", err)
	}
	for _, key := range []string{QueueUpload, QueueDelete} {
		if err := ch.QueueBind(QueueDead, key, DLXExchange, false, nil); err != nil {
			return fmt.Errorf("bind dead queue: %w", err)
		}
	}
	return nil
}
