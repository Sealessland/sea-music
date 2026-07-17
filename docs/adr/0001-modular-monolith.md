# ADR 0001：模块化单体与独立 Worker

- 状态：接受
- 日期：2026-07-13

## 决策

使用一个 Go API 进程承载 identity、video、social、discovery 模块，使用独立 Go Worker 处理 Outbox、事件消费、媒体任务和对账。各模块拥有独立包和 PostgreSQL schema，禁止导入其他模块的内部实现。

## 原因与后果

UGC 核心链路仍在快速迭代，跨模块事务和本地调试价值高于微服务独立部署。独立 Worker 隔离 CPU/IO 密集任务并允许单独扩容。代价是 API 模块共享发布节奏；当团队或负载边界明确后，可沿事件和 schema 所有权拆分。
