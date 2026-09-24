package broker

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// headerCarrier adapts AMQP headers to the OpenTelemetry
// TextMapCarrier, so the trace context crosses the broker: the
// publisher injects it into the message, the consumer extracts it and
// continues the same trace in the worker process.
type headerCarrier amqp.Table

func (c headerCarrier) Get(key string) string {
	if s, ok := c[key].(string); ok {
		return s
	}
	return ""
}

func (c headerCarrier) Set(key, value string) {
	c[key] = value
}

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
