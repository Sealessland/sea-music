# 可用于简历的可证实成果

- 独立设计并实现 Go 1.26 UGC 视频后端：身份、投稿状态机、S3 直传校验、真实 ffmpeg 转码/封面、审核发布、社交互动和三类发现 feed，使用 15 个可重复 PostgreSQL migration。
- 构建 Transactional Outbox/Inbox 可靠事件链路，覆盖 broker ack 窗口崩溃、重复消费、毒消息 DLQ/重放、租约超时恢复和计数漂移对账；正式 E2E 不使用同步旁路。
- 接入 HTTP/pgx/Redis/Kafka/ffmpeg OpenTelemetry、RED/USE 与业务 backlog 指标、Prometheus/Grafana/Tempo，并用真实 Collector→Tempo 查询验证 API 和 Worker traces。
- 在固定 seed 数据集上建立可重复压测；通过有界、过期感知的签名 URL 缓存，将视频详情读三次中位吞吐从 2998 提升到 3645 RPS（+21.6%），3,000 个 A/B 详情请求 0 错误。环境和限制见 [性能报告](performance/baseline.md)。

这些描述只引用仓库脚本可重放的结果，不声称生产 SLA、恰好一次投递或公网容量。
