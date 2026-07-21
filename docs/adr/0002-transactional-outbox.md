# ADR 0002：Transactional Outbox / Inbox

- 状态：接受
- 日期：2026-07-13

## 决策

所有需要发事件的业务写入与 Outbox 行放在同一个 PostgreSQL 事务中。Dispatcher 使用 `SKIP LOCKED`、租约、退避和 broker ack；消费者先以 event ID 写 Inbox，再应用副作用。毒消息进入死信，管理员可按稳定 event ID 重放。

## 原因与后果

数据库提交和外部消息 broker 发布无法组成普通原子事务。Outbox 消除“业务成功但事件丢失”，Inbox 消除重复投递的重复副作用。Kafka、RocketMQ 与 NATS JetStream 通过同一 `Publisher` / `Consumer` 契约接入，共享 Outbox、Inbox、重试与 DLQ 语义；系统提供至少一次投递和业务幂等，不声称端到端恰好一次。
