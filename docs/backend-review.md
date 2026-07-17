# 后端评审见解

一次对全部 Go 代码（约 1 万行、102 个文件）的逐模块评审记录。每条结论都有 `文件:行号` 证据支撑，可复核、可演示。
评审日期：2026-07-16。评审范围：`cmd/*`、`internal/*`、`deploy/observability`、`scripts/`、`docs/runbooks`。

## 总评

写路径是这个项目最强的部分：会话安全、事务纪律、事件骨架（Outbox/Inbox/DLQ/重放）和媒体管线的幂等设计都是生产级水准，而且每一环都有真实依赖（真实 Postgres/Redis/Kafka/ffmpeg）的集成测试佐证，不是纸面设计。

主要缺口集中在三处：**读路径**（跨 schema 直读绕过自己定的模块边界、N+1、缺缓存与分页）、**异步链路的尾部**（事件失败后的终点站没有出路、trace 在 dispatcher 断链）、以及若干**"管道建好但没接水龙头"的半成品功能**（计数展示、blocks、category、弹幕审核）。

## 一、值得保留并讲清楚的设计

1. **Refresh token 轮换 + 家族级重放撤销**：旋转时记录 `replaced_by` 链，`SELECT ... FOR UPDATE` 锁住会话行，发现已轮换 token 被重放就把整个 session family 撤销（`internal/identity/postgres.go:69-139`）；refresh token 库中只存 SHA-256，登录对不存在用户做 dummy hash 钝化枚举时序（`internal/identity/service.go:187-195,129-133`），密码用 Argon2id 恒定时间比较（`internal/identity/password.go:22-30`）。RFC 6819 推荐模式的完整落地，有集成测试（`internal/identity/registration_integration_test.go:93`）。
2. **Outbox/Inbox 可靠事件闭环**：业务写与事件同事务（`internal/video/postgres.go:113-125`、`internal/social/relations.go:55-104`）；dispatcher 用 `FOR UPDATE SKIP LOCKED` + 租约抢批、broker ack 后才标记 published（`internal/events/dispatcher.go:44-115`）；消费端 inbox 去重行与业务副作用同 DB 事务提交（`internal/events/inbox.go:29-54`）；毒消息隔离进 `dead_letters` 且 DLQ 事件本身也走 outbox（`internal/events/consumer.go:104-152`），管理员重放带角色校验和 `replay_count` 审计（`internal/events/replay.go:26-68`）。ack 窗口崩溃重发有真实 broker 故障注入测试证明副作用只执行一次（`internal/events/recovery_integration_test.go:14-77`）。
3. **状态机 + 乐观版本 + 审计的事务模板**：视频 7 态迁移内存校验与 DB `WHERE version=$2` 双保险，每次迁移写 `state_transitions`（`internal/video/postgres.go:70-134`）；finalize 把 asset 核验、状态推进、job 幂等入队、outbox、事件回链放在一个事务（`postgres.go:215-287`）。
4. **热分的写时懒衰减**：无需定时全量重算，单条 upsert 用 `calculated_at` 做指数衰减 `score = old * exp(-Δt/τ) + w`，`engagement_events` 主键天然去重（`internal/discovery/hot.go:53-87`）；DB 是权威，Redis 只是读路径，Redis 出错自动降级 DB 快照并显式打 `degraded` 标（`hot.go:133-154`）。
5. **计数的最终一致 + 对账闭环**：`GREATEST(x+delta,0)` 防负 upsert（`internal/social/counters.go:77-89`），周期 reconciler 从权威表重算（正确过滤软删评论和不可见弹幕）、drift 落审计表并进 Prometheus 指标（`internal/social/reconciliation.go:44-100`）。
6. **配置的 fail-fast 与生产护栏**：本地零配置可跑，但 production 下残留本地默认凭据直接拒绝启动（`internal/platform/config/config.go:262-276`）；token key 下限 32 字节、CORS 禁通配符、错误消息不含 secret；`LookupEnv` 注入让配置解析可单测（`config.go:112-117`）。
7. **限流的位置感**：认证先于限流，已认证用户按 `user:<id>` 桶、匿名按 IP 桶（`internal/appapi/ratelimit.go:163-176`）；fail-open/closed 按业务类区分——读放行、登录注册写拒绝（`internal/appapi/identity.go:30-32`）；Lua 令牌桶带时钟回拨守卫（`internal/platform/ratelimit/ratelimit.go:14-36`）。
8. **媒体管线的确定性幂等**：确定性 rendition key + `ON CONFLICT` upsert，租约心跳续约、失租立即 cancel 杀掉 ffmpeg 子进程（`internal/video/processing.go:191-231`、`internal/video/jobs.go:215-223`），"worker 崩了另一个接手"有集成测试直接验证（`internal/video/ffmpeg_integration_test.go:20-66`）。
9. **架构边界固化进测试**：AST 测试禁止 domain 模块互相 import（`internal/architecture/boundaries_test.go:42-92`），实测四模块零越界 import、零循环依赖。
10. **可观测性的意图完整**：otelpgx/redisotel/kotel 双侧埋点 + 手动 ffmpeg span + outbox 信封携带 traceparent 的设计意图是对的（`cmd/api/main.go:106`）；Grafana 预置 outbox 积压、drift、最老事件年龄等 6 面板。遗憾在执行有断点，见问题 B2。

## 二、问题清单（按主题分组、组内按严重度排序）

### A. 安全与正确性

1. **Access token 无撤销通道，且无登出口【高】**：`Verify` 纯无状态不查会话表（`internal/identity/token.go:62-96`），refresh 家族被撤销后，已签发的 access token 在默认 15 分钟内照样可用；角色降级同样要等过期。路由表无 logout（`internal/appapi/identity.go:29-34`），前端只是清本地状态（`internal/appapi/web/app.js:503`）。修法：登出端点 + 会话版本号进 JWT、验证时比对，或接受短 TTL 并在文档中明示权衡。
2. **互动不变式不对称：弹幕校验了发布态，评论/点赞/收藏没有【高】**（已修复 2026-07-16）：`CreateDanmaku` 校验视频必须 published（`internal/social/danmaku.go:51-54`），而 `CreateComment`、`SetLike`、`SetFavorite` 只靠 FK（`internal/social/comments.go:49-102`、`internal/social/relations.go:31-43`）。draft/withdrawn/failed 视频可以被点赞评论，事件照常入 outbox，污染计数和热榜。修复：`setRelation` 在 `enabled=true` 时校验 published、`CreateComment` 对齐 danmaku 同款校验，取消操作不校验以便清理关系（`internal/social/visibility_integration_test.go`）。
3. **全链路无 panic recovery【中】**（已修复 2026-07-16）：`gin.New()` 之后没有 Recovery 中间件（`internal/platform/httpserver/handler.go:27-28`），handler panic 时客户端拿到断连而非带 request_id 的 JSON 错误。修复：`recoverPanic` 中间件注册在 requestLog 之后、所有路由之前，panic 打带 stack/request_id 的 error 日志并按统一错误模型返回 500（`internal/platform/httpserver/recovery_test.go`）。
4. **限流覆盖面窄 + IP 识别在反代后失效【中】**：`GinWrap` 只挂在 identity 四条路由；video/social 写接口（点赞、关注、评论、建草稿）无平台限流。匿名识别直接用 `request.RemoteAddr`（`ratelimit.go:167-171`），无 `TrustedProxies`/XFF 处理——LB 后所有匿名流量汇聚成一个桶，而 login 是 fail-closed，会被整体锁死 503。
5. **`/metrics` 无鉴权暴露内部运行状态【低】**：outbox 积压、连接池、processing jobs 全公开（`internal/appapi/ratelimit.go:103-161`），且注册在公网同一路由器。
6. **social 写接口错误映射粗糙【低】**：`writeResult` 把所有错误（包括 DB 宕机）一律映射成 422 `invalid_social_relation`，无 default→500 分支（`internal/appapi/social.go:160-167`），会误导客户端与告警。

### B. 消息与异步链路

1. **Outbox `failed` 是终点站，没有重放路径【高】**（已修复 2026-07-16）：dispatcher 重试 10 次耗尽 → `state='failed'`（`internal/events/dispatcher.go:117-144`），事件永远到不了 Kafka，而重放工具只查 `dead_letters` 表（`internal/events/replay.go:32-36`）。业务已提交、事件永久卡住，仅有一个无告警的指标。连带后果：processing job 只能靠 `video.source_finalized` 事件激活（`internal/video/events.go:33-36`），该事件若进 DLQ 无人重放，job 永远卡 `queued`——`ClaimProcessingJob` 不扫 queued（`internal/video/jobs.go:42`），无兜底清扫器，视频停在 uploaded 永不转码。修复：新增 admin 端点 `POST /api/v1/admin/outbox-events/:event_id/replay` 把 failed 事件重置回 pending 由 dispatcher 自然重投（`internal/events/replay.go`）；worker 新增周期兜底循环把滞留 queued 的 job 激活为 pending（`internal/video/jobs.go` 的 `ActivateStaleQueuedJobs` + `cmd/worker/main.go`，`SEA_MEDIA_QUEUED_ACTIVATION_INTERVAL` 默认 30s、`SEA_MEDIA_QUEUED_ACTIVATION_THRESHOLD` 默认 2min）。
2. **端到端 trace 在 dispatcher 处断链，与 runbook 宣称不符【中】**：dispatcher 手动写入的 `traceparent` header（`dispatcher.go:172-175`）会被 kotel `OnProduceRecordBuffered` 的 `Inject` 覆盖（kotel@v1.7.0 `carrier.go:30-37` 的 `Set` 替换同名 key），而 dispatcher 循环 ctx 无 span，publish span 是新 root；消费侧也没有任何代码读 `envelope.TraceParent` 回填。`docs/runbooks/fault-drills.md:19` 宣称的"stable trace context across API, Kafka, consumer, SQL, Redis, and ffmpeg spans"在 API→worker、worker→ffmpeg 两跳都不成立。要么修传播（dispatcher 用信封 traceparent 起 span），要么删死代码并修正 runbook。
3. **dispatcher 吞吐与背压缺失【中】**：单 goroutine 批内串行 `ProduceSync`，一条失败整批放弃、剩余行等租约过期（默认 1min）才被重领（`dispatcher.go:209-228`）；`UnknownTopicRetries(-1)` 且无 per-event 超时，可长期阻塞在单条记录上。消费端 `PollRecords(1)` + 进程内 sleep 退避，毒消息造成队头阻塞（`internal/events/consumer.go:51,82-88`）。
4. **优雅停机丢弃进行中的转码【低】**：`FailProcessingJob` 用已取消的 ctx 记录失败，大概率失败，job 要等租约过期（默认 2min）才被重领——每次部署留下最长 2 分钟的"假 processing"窗口（`internal/video/processing.go:191-231`）。
5. **admin 重放绕过 outbox【点名】**：replay 从 API 进程直接 Produce 到 Kafka（`internal/events/replay.go:50`），与全系统 outbox 纪律不一致；崩溃窗口产生的重复由 inbox 兜住，可接受，但值得知道。

### C. 读路径性能

1. **热榜候选可见性过滤是 N+1【高】**（已修复 2026-07-16）：`visibleCandidates` 逐 id 单查（`internal/discovery/hot.go:216-241`），候选量 `limit*3`：limit=20 → 最多 60 次 DB RT，limit=100 → 300 次。修复：一次 `WHERE id = ANY($1::text[]::uuid[])` 批量查询，Go 侧按原 ZSET 排名顺序重排，过滤语义逐条对齐原实现（`internal/discovery/hot_integration_test.go` 覆盖混合可见性与顺序保持）。
2. **评论列表对每个顶层评论单查回复【中】**：一页最多 101 条查询（`internal/social/comments.go:185,205-226`），可一次 `WHERE parent_id = ANY($1)` 后在内存分组。
3. **推荐是无截断全表计算排序，且无分页【中】**：`Recommend` 对全部 published 视频 LEFT JOIN + 计算值排序（`internal/discovery/recommendation.go:25-50`），handler 不接收 cursor，永远返回空 `NextCursor`（`internal/appapi/discovery.go:48-52`）。目录一大就是每请求 O(N log N)。hot feed 同样无游标。
4. **对账扫描方向反了且缺索引【中】**：`ReconcileBatch` 按 `ORDER BY updated_at ASC` 取最久未更新的视频——漂移概率最低的一批，且每轮重复扫同一批，活跃视频永远轮不到；`video.videos.updated_at` 无索引，每个间隔一次全表排序（`internal/social/reconciliation.go:106-108`）。应改为按"最近有事件"驱动或至少 DESC + 索引。
5. **读路径零缓存防护【低】**：无空值缓存、无 singleflight、无逻辑过期；视频详情每请求打 DB + 2 次 presign（`internal/video/publication.go:79-96`），following feed 每请求直查 DB。当前规模可接受，但要有意为之而非默认缺失。

### D. 半成品功能（管道建了，水龙头没接）

1. **计数管线产出无消费者【中】**：`sea:counters:*` HASH 只写不读（生产代码无 `HGet`，仅测试读）、从不设 TTL 线性涨内存；更根本的是**没有任何 API 返回计数**——`PublicVideo`、`FeedItem` 都没有计数字段（`internal/video/publication.go:22-31`、`internal/discovery/feed.go:20-28`）。整条计数/对账链路目前是 dead feature：要么接展示，要么关掉 Redis 写。
2. **`social.blocks` 无写入路径【低】**：三个 feed 的 block 过滤因此恒为空集，只有测试写入（`internal/discovery/visibility_integration_test.go:40`）。
3. **`category` 恒为迁移默认值 `'general'`【低】**：`CreateDraft` 不收 category（`internal/video/postgres.go:38-41`），推荐的 `category_affinity` 和冷启动多样化因此退化。
4. **弹幕软删除是半成品【低】**：`visible` 列只有读取过滤，全库无代码置 false（`internal/social/danmaku.go:101`），没有弹幕审核/删除 API。
5. **两处小名实不符【低】**：热榜 Redis 正常但 key 为空（冷启动/24h 过期）时走最新发布补齐却 `Degraded:false`（`internal/discovery/hot.go:122-131`）；`halfLifeSeconds` 代入 `exp(-Δt/τ)` 后 12h 只衰减到 1/e≈37% 而非 50%，变量名与公式不符（`hot.go:69-79`）。

### E. 运维与平台

1. **事件与会话表无保留策略【中】**：`eventing.outbox`、`eventing.inbox`、`discovery.engagement_events`、`identity.sessions` 均无清理/TTL，长期运行必然膨胀。
2. **有指标无告警，日志不进管道【中】**：Prometheus 只有 scrape 无 rule_files；应用日志不走 OTLP，collector logs pipeline 只接 debug exporter，无集中日志，Tempo 也未配 trace-to-logs（`deploy/observability/otel-collector.yaml:40-43`）。
3. **迁移工具的隐性 30s 上限【低】**：30s ctx 整体包裹所有迁移（`cmd/migrate/main.go:32,48`），未来重数据迁移会被整体切断；无 down 命令；已应用迁移 checksum 被改动会硬失败卡部署（`internal/platform/migrate/migrate.go:115-117`）。
4. **verify.sh 的 RED 指标检查沿用旧标签格式【低】**（已修复 2026-07-16）：路由标签在 gin 迁移后不再带方法前缀（`GET /api/v1/me` → `/api/v1/me`，旧格式见 `artifacts/benchmarks/*/metrics-after.prom`），检查串未同步导致 `make verify` 在该检查点必挂；已对齐为 gin 的 `FullPath` 格式。

## 三、建议行动清单

**P0（正确性/安全，改动小收益大）**

- 互动写入统一校验视频可见性，对齐 danmaku 已有的 published 检查（A2）。
- 加 `gin.Recovery()` 类中间件，panic 返回统一错误模型并带 request_id（A3）。
- 热榜 N+1 改 `WHERE id = ANY($1)` 批量查询（C1）。
- 给 outbox `failed` 状态接重放路径（复用 admin replay，扩展查询到 outbox failed 行），并给 queued job 加兜底激活扫描（B1）。

**P1（体验与规模）**

- 登出端点 + access token 撤销策略（会话版本号或接受短 TTL 并文档化）（A1）。
- 限流挂到 video/social 写接口；代理解析 XFF 或配置 TrustedProxies（A4）。
- 评论回复批量查询；推荐加分页与候选截断（C2/C3）。
- 修 trace 传播：dispatcher 用信封 traceparent 起 span，打通 API→worker→ffmpeg（B2）。
- 计数接 API 展示或下线 Redis 写（D1）；对账扫描改方向并补索引（C4）。

**P2（运维成熟度）**

- outbox/inbox/sessions 保留策略与定时清理；Prometheus 告警规则（outbox 积压、failed 数、drift）；日志进 OTLP（E1/E2）。
- blocks、category、弹幕审核：要么补写入路径，要么从 API/文档中摘除，避免"看起来支持"（D2-D4）。
- dispatcher 批量内并发 + per-event 超时；停机时用 `context.WithoutCancel` 记录转码失败（B3/B4）。
- `/metrics` 鉴权或独立端口（A5）；social 写接口错误映射补 500 分支（A6）。

## 四、面试视角：这些点怎么讲

这份评审本身就是最好的"项目拷打"准备——每个问题都是面试官可能挖的坑，提前想好答案：

- **"你的消息可靠性做到什么程度？"** 讲至少一次 + 幂等 = 有效一次，以及 ack 窗口崩溃的故障注入测试；主动说出 failed 终点站无 replay 这个已知缺口和改进方案，比被问出来强得多。
- **"怎么实现登出/令牌撤销？"** 诚实回答当前是 15 分钟无状态窗口，给出会话版本号方案和对无状态验签的性能权衡。
- **"热榜怎么算？实时吗？"** 写时懒衰减 + Redis 读路径 + 显式降级标志是亮点；N+1 是已知优化点，说得出 `ANY($1)` 修法。
- **"模块边界怎么保证？"** AST 边界测试；同时坦承 SQL 层是共享库 + schema 约定，读路径有跨 schema join，这是模块化单体的已知妥协，拆分路径沿 schema 所有权走（呼应 ADR 0001）。
- **"trace 怎么跨 Kafka？"** 设计意图（信封携带 traceparent）+ 当前断点（kotel 覆盖 header）+ 修法，展示你对埋点库行为的理解深度。
- **"计数为什么最终一致？怎么兜底？"** 三段式（同事务事件 → 幂等投影 → 周期对账 + 审计 + 指标）；主动讲对账扫描方向的 bug 和修法，体现自我审查能力。

相关文档：[架构与运行链路](architecture.md)、[ADR 0001](adr/0001-modular-monolith.md)、[ADR 0002](adr/0002-transactional-outbox.md)、[ADR 0003](adr/0003-direct-upload-media.md)、[面试学习手册](interview-study-guide.md)。
