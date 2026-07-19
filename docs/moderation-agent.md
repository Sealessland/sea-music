# Moderation Agent

Moderation Agent 是独立 Go gRPC 进程。它生成可审计的审核证据，但不拥有视频发布权限。默认运行在 `shadow` 模式；未配置模型供应商时返回 `escalate`，不会伪装成自动审核成功。

## 可靠链路

```mermaid
sequenceDiagram
    participant Media as Media Worker
    participant DB as PostgreSQL
    participant Kafka
    participant Dispatch as Dispatch Worker
    participant Agent as gRPC Agent
    participant Eino as Eino Provider

    Media->>DB: 完成转码 + Outbox(video.ready_for_moderation)
    DB->>Kafka: Dispatcher 发布（broker ack 后确认）
    Kafka->>Dispatch: Inbox 幂等创建 dispatch job
    Dispatch->>Agent: StartReview(request_id = event_id)
    Agent->>DB: 幂等创建 review operation
    Agent->>Eino: reviewer 结构化分类
    Eino-->>Agent: reviewer candidate
    Agent->>Eino: critic 独立反证
    Eino-->>Agent: critic candidate
    Agent->>Agent: 一致性 + 置信度策略门禁
    Agent->>DB: 当前 lease owner 完成 operation
    Dispatch->>Agent: GetReview(operation_id)
    Dispatch->>DB: 保存 shadow 结果
```

- `request_id` 是 Outbox event UUID；同 ID、同输入返回同一 operation，同 ID、不同输入返回冲突。
- operation 和 dispatch job 都使用 `FOR UPDATE SKIP LOCKED`、租约 owner、租约过期接管和有界失败预算。
- Provider 调用不发生在 Inbox 数据库事务内；Kafka 重投只会命中唯一 `event_id`。
- 旧 worker 失去租约后不能提交结果，避免覆盖接管者的证据。

## gRPC 契约

契约位于 `api/proto/sea_music/moderation/v1/moderation.proto`，通过 Buf STANDARD lint 和固定版本插件生成。

- `StartReview`：启动或幂等取得异步审核 operation。
- `GetReview`：查询 `pending/running/completed/failed/cancelled` 状态及结构化结果。
- gRPC health service 同时报告整体服务和 `sea_music.moderation.v1.ModerationService`。

模型结果中的 provider/model 字段由服务端写入，`policy_version` 来自已验证请求；`can_publish` 在领域层强制为 `false`。

## Reviewer、Critic 与策略门禁

`SEA_MODERATION_PROVIDER=openai` 启用 CloudWeGo Eino 官方 OpenAI ChatModel 组件，也可通过 base URL 连接兼容服务。工作流是有固定预算的 reviewer→critic 两阶段图，不使用无界 ReAct 循环：reviewer 先产生候选证据；critic 同时读取原始请求和候选，专门检查误判、遗漏、引用/教育/艺术语境和无证据的过度自信；最后由纯 Go 策略引擎计算结果。

标题、描述、资产字段和 reviewer 候选全部作为 JSON 不可信数据传入。两个阶段的非法 verdict、置信度、finding 或非 JSON 输出都会 fail-closed 并消耗持久化重试预算。即使两个模型阶段一致，approve 仍需达到 `SEA_MODERATION_APPROVE_THRESHOLD`（默认 `0.90`），reject 需达到更严格的 `SEA_MODERATION_REJECT_THRESHOLD`（默认 `0.95`）；任何分歧、escalate 或低置信度都统一升级人工。

结果中的 `strategy`、`votes` 和 `checks` 保存两阶段原始结论、合并 finding 及每个确定性门禁，随 operation JSON 持久化并通过 gRPC 返回，因此无需重新调用模型即可回放“为什么作出该结论”。Agent 始终写入 `can_publish=false`。

`internal/moderation/testdata/decision_policy_eval.json` 是版本化离线评测集，覆盖一致通过、低置信度、reviewer/critic 分歧和主动升级等边界；普通 `go test ./...` 会逐例执行，因此策略阈值或归并规则变化必须显式更新评测预期。

当前 provider 覆盖标题和描述；source asset URI、类型与 SHA-256 作为证据引用，尚未下载视频并执行帧/音轨多模态分析。

## 运行与观测

```sh
SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
go run -buildvcs=false ./cmd/moderation-agent
```

默认端点：gRPC `:9090`，health/metrics HTTP `:9091`。Prometheus 中间件导出每个 RPC 的请求总数、状态码和 handling-time histogram，并额外提供 `sea_music_moderation_agent_evaluations_total`、`sea_music_moderation_agent_policy_check_failures_total`、`sea_music_moderation_agent_errors_total` 和端到端评估延迟；OpenTelemetry 将一次审核拆成 `evaluate`、`reviewer`、`critic` span，只记录策略、结论和置信度，不记录用户正文。gRPC client/server 与 PostgreSQL 也在同一 trace 中。`make verify` 会真实启动 Agent，等待 readiness，走完整 Outbox→Kafka→Inbox→gRPC→结果落库链，并断言成功计数已采集。

本地开发默认 `SEA_MODERATION_INSECURE=true`。生产环境会拒绝 plaintext，必须配置 `SEA_MODERATION_TLS_CERT_FILE`、`SEA_MODERATION_TLS_KEY_FILE` 和 `SEA_MODERATION_TLS_CA_FILE`，服务端要求并验证客户端证书。
