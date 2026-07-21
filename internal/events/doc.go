// Package events owns broker-independent event delivery semantics.
//
// Read the message-queue implementation in this order:
//
//   - broker.go defines Publisher and Consumer and selects an adapter.
//   - kafka.go contains the complete Kafka producer/consumer adapter.
//   - rocketmq.go contains the complete RocketMQ producer/consumer adapter.
//   - jetstream.go contains the complete NATS JetStream adapter.
//   - dispatcher.go owns Outbox leasing and delivery state transitions.
//   - consumer.go owns shared Inbox retries and transactional DLQ quarantine.
//
// Broker adapters translate transport acknowledgements only. Outbox, Inbox,
// retry, and dead-letter behavior must remain shared so every implementation
// provides the same at-least-once contract.
package events
