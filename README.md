# Sea Music

面向 Bilibili 类 UGC 视频社区核心业务的 Go 后端项目，采用**模块化单体 API + 独立 Worker** 架构，覆盖身份鉴权、对象存储直传、媒体处理（ffprobe/ffmpeg）、审核发布、可靠事件投递（Transactional Outbox/Inbox/DLQ/重放）、社交互动、内容发现、全链路可观测与故障恢复。

所有关键链路运行在真实依赖（PostgreSQL / Redis / Kafka / SeaweedFS / ffmpeg）之上，核心设计均有基于真实依赖的集成测试、故障注入演练或可重放的性能数据佐证。

- 规模：约 1.05 万行 Go（105 个文件，其中 43 个测试文件）、15 个版本化迁移、20 个业务端点
- 形态：单 API 进程承载 4 个业务模块，独立 Worker 负责事件分发/消费、媒体任务与计数对账
- 验证：`make verify` 真实依赖 E2E、`make fault-drill` 故障演练、`make benchmark` k6 open-model 压测

## 目录

- [业务功能](#业务功能)
- [技术栈](#技术栈)
- [系统架构](#系统架构)
- [核心设计](#核心设计)
- [性能与压测](#性能与压测)
- [快速开始](#快速开始)
- [验证与演练](#验证与演练)
- [API 概览](#api-概览)
- [项目结构](#项目结构)
- [配置](#配置)
- [已知限制](#已知限制)
- [文档索引](#文档索引)

## 业务功能

| 模块 | 能力 |
|---|---|
| Identity | 注册/登录、双 token（access + refresh）、refresh token 轮换与家族级重放撤销、Argon2id 密码散列、基于角色的授权 |
| Video | 投稿草稿、S3 预签名直传授权、finalize 校验（长度/类型/SHA-256）、7 态状态机（draft→uploaded→processing→review→published→withdrawn/failed）、审核发布、撤稿、真实 ffmpeg 转码与封面生成 |
| Social | 点赞、收藏、关注、评论（含回复）、弹幕；计数异步投影 + 周期对账修复漂移 |
| Discovery | 热门榜（写时懒衰减热分）、关注流、个性化推荐；三类 feed 统一执行发布态/审核可见性/block 过滤；Redis 故障时显式降级 |
| Events | Transactional Outbox 分发、Inbox 幂等消费、毒消息 DLQ、管理员重放（带审计） |
| Platform | 配置 fail-fast 与生产护栏、Lua 令牌桶限流、版本化迁移、OpenTelemetry 遥测、内嵌 Web 前台 |

仓库内嵌一个零构建的 Web 前台（`internal/appapi/web`，go:embed 同源提供）：匿名热门流、注册/登录、推荐、关注流、播放详情、点赞/收藏/关注/评论/弹幕均可直接操作，便于本地演示与联调。

## 技术栈

- **语言/框架**：Go 1.26、Gin（含官方 CORS middleware）、pgx/v5、go-redis v9、franz-go（Kafka）、aws-sdk-go-v2（S3）
- **数据与基础设施**：PostgreSQL 18、Redis 8、SeaweedFS（S3 兼容对象存储）、Apache Kafka 4（KRaft）
- **可观测**：OpenTelemetry（otelhttp/otelpgx/redisotel/kotel + 手动 span）、Prometheus Go client、Grafana、Tempo、OTel Collector
- **安全**：Argon2id（恒定时间比较）、HMAC 签名 token、SHA-256 存储 refresh token
- **媒体**：系统 ffprobe/ffmpeg 真实转码
- **工程**：Docker Compose 一键环境、k6 open-model 压测、固定 seed 数据集、架构边界 AST 测试

## 系统架构

```mermaid
flowchart LR
    Client[Web / App] --> API[Go API]
    API --> ID[Identity]
    API --> Video[Video]
    API --> Social[Social]
    API --> Discovery[Discovery]
    ID & Video & Social & Discovery --> PG[(PostgreSQL)]
    API --> Redis[(Redis)]
    API --> S3[(SeaweedFS S3)]
    PG --> Outbox[Transactional Outbox]
    Outbox --> Kafka[(Kafka)]
    Kafka --> Worker[Go Worker]
    Worker --> PG
    Worker --> S3
    Worker --> FF[ffprobe / ffmpeg]
    API & Worker --> OTel[OTel Collector]
    OTel --> Tempo[Tempo]
    OTel --> Prom[Prometheus]
    Prom --> Grafana[Grafana]
```

关键运行链路（完整版见 [docs/architecture.md](docs/architecture.md)）：

1. 客户端创建草稿，API 生成用户隔离的对象 key 和短期 S3 PUT 预签名 URL，客户端直传源文件。
2. finalize 重新读取对象并核验长度、Content-Type 和 SHA-256，在**同一事务**中推进状态机、幂等入队转码任务并写 Outbox。
3. Dispatcher 只有收到 Kafka ack 后才确认 Outbox 行；Worker 通过 Inbox 去重并领取带租约的处理任务。
4. Worker 运行真实 ffprobe/ffmpeg，上传确定性 rendition 与封面，将视频推进到审核态；审核通过后才对外可见。
5. 点赞/收藏/关注/评论/弹幕先写权威关系及 Outbox，消费者异步投影计数和热门分数；周期对账修复漂移。

关键决策记录：[ADR 0001 模块化单体](docs/adr/0001-modular-monolith.md)、[ADR 0002 Transactional Outbox](docs/adr/0002-transactional-outbox.md)、[ADR 0003 直传与真实媒体处理](docs/adr/0003-direct-upload-media.md)。

## 核心设计

每项设计均标注代码位置，可直接复核。

1. **可靠事件投递（Outbox/Inbox/DLQ/重放）**：业务写与事件同事务提交（`internal/video/postgres.go:215`、`internal/social/relations.go:55`）；dispatcher 用 `FOR UPDATE SKIP LOCKED` + 租约抢批，broker ack 后才标记 published（`internal/events/dispatcher.go:44`）；消费端 inbox 去重行与副作用同事务（`internal/events/inbox.go:29`）；毒消息隔离进 `dead_letters` 并发布到真实 `.dlq` topic，管理员重放带角色校验与 `replay_count` 审计（`internal/events/replay.go:26`）。系统提供**至少一次投递 + 业务幂等**（即有效一次），不声称端到端恰好一次。ack 窗口崩溃重发有真实 broker 故障注入测试，证明副作用只执行一次（`internal/events/recovery_integration_test.go`）。
2. **会话安全**：refresh token 轮换时记录 `replaced_by` 链，`SELECT ... FOR UPDATE` 锁会话行，发现已轮换 token 被重放即撤销整个 session family（RFC 6819 推荐模式，`internal/identity/postgres.go:69`）；库中只存 SHA-256；登录对不存在用户做 dummy hash 钝化枚举时序；密码使用 Argon2id 恒定时间比较。
3. **状态机 + 乐观版本 + 审计**：视频 7 态迁移做内存校验与 DB `WHERE version=$2` 双保险，每次迁移写 `state_transitions` 审计行（`internal/video/postgres.go:70`）。
4. **媒体管线确定性幂等**：确定性 rendition key + `ON CONFLICT` upsert；任务租约心跳续约，失租立即 cancel 并终止 ffmpeg 子进程（`internal/video/processing.go:191`）；worker 崩溃后由其他实例接管有集成测试直接验证（`internal/video/ffmpeg_integration_test.go`）；另有兜底循环激活滞留 queued 的任务，消除"事件丢失 → 永不转码"的死角。
5. **热分写时懒衰减与显式降级**：无需定时全量重算，单条 upsert 用 `calculated_at` 做指数衰减 `score = old * exp(-Δt/τ) + w`，事件主键天然去重（`internal/discovery/hot.go:53`）；DB 是权威，Redis 仅作读路径，Redis 故障时自动降级为 DB 快照并在响应中显式携带 `degraded` 标志。
6. **计数最终一致与对账**：`GREATEST(x+delta,0)` 防负 upsert；周期 reconciler 从权威表重算（正确过滤软删评论/不可见弹幕），drift 落审计表并导出 Prometheus 指标（`internal/social/reconciliation.go:44`）。
7. **限流与降级策略**：认证先于限流，已认证用户按 `user:<id>` 桶、匿名按 IP 桶；fail-open/closed 按业务区分——读放行、登录注册写拒绝；Lua 令牌桶带时钟回拨守卫（`internal/platform/ratelimit/ratelimit.go:14`）。
8. **配置 fail-fast 与生产护栏**：本地零配置可运行，`SEA_ENV=production` 下残留本地默认凭据将直接拒绝启动；token key 下限 32 字节、CORS 禁通配符（`internal/platform/config/config.go:262`）。
9. **架构边界测试**：AST 测试禁止 domain 模块互相 import（`internal/architecture/boundaries_test.go`），当前四个模块零越界、零循环依赖。
10. **回归驱动的性能优化**：通过 trace 定位到详情请求重复生成两个 SigV4 URL 的 CPU 热点，先补充缓存容量上限回归测试，再实现过期感知、最多 10,000 项的进程内缓存；`SEA_S3_DISABLE_DOWNLOAD_CACHE=true` 可一键回退，便于故障隔离与 A/B 对比（ADR 0003）。

全部代码的逐模块评审（含已修复项与已知缺口）见 [docs/backend-review.md](docs/backend-review.md)。

## 性能与压测

以下数据均可通过仓库脚本重放，基线原始证据见 `artifacts/performance/`；测试口径与适用边界见 [docs/performance/baseline.md](docs/performance/baseline.md)（单机环境，不构成生产容量或 SLA 结论）。

<!-- benchmark-ci:start -->
> 最近一次通过门禁的 CI 基准：[workflow run](https://github.com/Sealessland/sea-music/actions/runs/29649105720) · `56cc73a8240f` · 2026-07-18T15:05:28Z

口径：`grafana/k6:2.0.0`、`constant-arrival-rate`、目标 200 RPS、持续 30s、`pareto80` 分布、200 个视频；每个对照组均执行，表中为重复运行中位数。共享 GitHub runner 数据仅用于回归比较，不代表生产 SLA。

| 对照组 | 重复次数 | 实际 QPS | P95 | P99 | 错误率 | Dropped | 阈值 |
|---|---:|---:|---:|---:|---:|---:|---|
| `cache` | 3 | 200.03 | 0.89 ms | 1.66 ms | 0.0000% | 0 | 通过 |
| `no-cache` | 3 | 200.02 | 1.20 ms | 2.02 ms | 0.0000% | 0 | 通过 |

缓存相对无缓存：P95 `-25.89%`，P99 `-17.74%`（负值表示延迟降低）。
<!-- benchmark-ci:end -->

- 固定 seed `20260713` 数据集：1,000 用户、500 视频、5,000 关注、4,000 点赞、1,500 收藏、1,000 评论、1,500 弹幕。
- **签名 URL 缓存 A/B**（closed-model，三次中位数）：视频详情吞吐 2998 → 3645 RPS（+21.6%），p99 降低 12.0%，3,000 个 A/B 请求 0 错误。
- **k6 open-model 复测**（`constant-arrival-rate`，pareto80 访问分布，500 RPS）：缓存组 p95 6.1ms vs 无缓存 36.0ms（-83%），p99 13.7ms vs 92.7ms（-85%），`dropped_iterations=0`，错误率 0。方法见 [benchmark-methodology](docs/performance/benchmark-methodology.md)。
- **写突发恢复**：500 个突发关系请求后，Outbox 全链路（pending + in-flight publishing）恢复 976ms，最终三种状态均为 0；负载后 SQL 池未见饱和。

## 快速开始

要求：Go 1.26、Docker Compose（或兼容的 Podman Compose）、curl、ffmpeg/ffprobe（媒体链路与完整测试需要）。

```sh
make bootstrap
```

该命令启动 PostgreSQL、Redis、SeaweedFS S3 API 和 Apache Kafka，应用数据库迁移，并装载固定 seed 的开发 fixture。

启动 API：

```sh
SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL='postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable' \
SEA_REDIS_URL='redis://:local-redis-password@127.0.0.1:26379/0' \
SEA_HTTP_ADDRESS=127.0.0.1:8080 \
go run -buildvcs=false ./cmd/api
```

- 前台首页：<http://127.0.0.1:8080/>（匿名热门流，以及注册/登录、推荐、关注流、播放、互动）
- 本地 `8080` 被占用时可设置 `SEA_HTTP_ADDRESS=127.0.0.1:38080`
- Worker 用同样的环境变量运行 `go run -buildvcs=false ./cmd/worker`

可选观测栈：

```sh
docker compose --profile observability up -d --wait
```

- Grafana: <http://127.0.0.1:33000>（预置 outbox 积压、drift、最老事件年龄等 6 面板）
- Prometheus: <http://127.0.0.1:39090>，Tempo: <http://127.0.0.1:33200>
- OpenTelemetry gRPC/HTTP: `127.0.0.1:34317` / `127.0.0.1:34318`

## 验证与演练

```sh
make verify                # 重建 8 个测试数据库，跑全部包测试 + 正式 API 真实 E2E
make verify-observability  # Collector→Tempo 真实查询到 API/Worker traces
make fault-drill           # broker 宕机、ack 窗口崩溃、毒消息、Redis 降级、worker 中断接管
make loadtest              # 固定 seed 的详情读/点赞突发/backlog 恢复 smoke
make benchmark             # k6 constant-arrival-rate 正式压测，不可变归档 + SHA256SUMS
make final-verify          # 从空白数据卷重放全部验证流程（会先删除本项目本地数据）
```

性能工作流在真实 PostgreSQL、Redis、Kafka、SeaweedFS、API 与 Worker 上依次执行全部 A/B 对照组；阈值失败会先上传完整归档再阻断 CI。主分支或定时任务通过后，机器人会更新上方 README 指标表；PR 只验证并展示 Job Summary，不回写分支。

`make verify` 的 E2E 覆盖完整业务纵切：注册登录真实用户 → 生成真实 MP4 → S3 直传与校验 → Outbox→Kafka→Inbox → ffprobe/ffmpeg → 审核发布 → 签名播放 URL 探测 → 社交/发现 → 撤稿过滤 → 漂移修复 → token 重放撤销 → 限流断言。

## API 概览

机器可读契约见 [api/openapi.json](api/openapi.json)，调用示例见 [docs/api-examples.md](docs/api-examples.md)。错误统一为 `{"error":{"code","message","details?"},"request_id"}`，限流响应额外携带 `Retry-After`。

- 身份：`POST /api/v1/users`、`POST /api/v1/sessions`、`POST /api/v1/sessions/refresh`、`GET /api/v1/me`
- 投稿：`POST /api/v1/videos`、`POST /api/v1/videos/{id}/uploads`、`POST /api/v1/videos/{id}/finalize`、`POST /api/v1/videos/{id}/review`、`POST /api/v1/videos/{id}/withdraw`、`GET /api/v1/videos/{id}`
- 社交：`PUT/DELETE .../like`、`.../favorite`、`/api/v1/users/{id}/follow`、`POST/GET .../comments`、`DELETE /api/v1/comments/{id}`、`POST/GET .../danmaku`
- 发现：`GET /api/v1/feed/hot`、`/feed/following`、`/feed/recommendations`
- 运维：`POST /api/v1/admin/dead-letters/{id}/replay`、`/livez`、`/readyz`、`/metrics`

## 项目结构

```
cmd/
  api/       # HTTP API：main.go 仅进程入口，app.go 显式装配依赖
  worker/    # 后台进程：app.go 装配资源，loops.go 承载可取消循环
  migrate/   # 数据库迁移（15 个版本化 SQL，checksum 校验）
  fixture/   # 固定 seed 开发/压测数据集装载
  loadtest/  # 负载 smoke runner
  testdb/    # 隔离测试数据库管理
internal/
  identity/    # 账号、会话、token 轮换与撤销、授权
  video/       # 投稿状态机、直传、对象存储、ffmpeg 管线
  social/      # 关系、评论、弹幕、计数与对账
  discovery/   # 热门/关注/推荐 feed 与可见性过滤
  events/      # Outbox dispatcher、Inbox 消费、DLQ、重放
  appapi/      # HTTP 适配层（Gin）：解析/鉴权/校验/响应映射
  platform/    # 配置、迁移、遥测、限流、fixture 等横切设施
    config/    # config.go 模型、load.go 加载、validate.go 校验、parse.go 转换
  architecture/# 模块边界 AST 测试
api/openapi.json          # OpenAPI 契约
deploy/observability/     # Collector/Prometheus/Grafana/Tempo 配置与预置仪表盘
benchmarks/k6/            # k6 压测脚本
scripts/                  # bootstrap/verify/fault-drill/benchmark 等可重放脚本
artifacts/performance/    # 性能基线原始证据（benchmarks/ 大体积归档本地生成，不入库）
docs/                     # 架构、ADR、评审、性能、runbook、验证报告
openspec/                 # spec-driven 开发过程材料
```

依赖方向保持为 `cmd → appapi/platform → domain`：`cmd` 是组合根，负责进程生命周期和跨模块装配；`appapi` 只做传输层适配；业务规则与存储接口归属各领域包；`platform` 不反向依赖业务领域。领域包之间不得直接导入，该约束由 `internal/architecture/boundaries_test.go` 自动检查。包内优先按职责拆分文件，而不是增加只有一层实现的子包，避免 Java 式目录深度和无意义接口。

## 配置

本地开发零配置可运行（默认值指向 compose 依赖）；所有配置均为 `SEA_*` 环境变量，解析集中在 `internal/platform/config/`，可注入 `LookupEnv` 进行单测。常用项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `SEA_ENV` | `development` | `production` 下启用严格护栏（禁本地默认凭据、CORS 必须显式） |
| `SEA_AUTH_TOKEN_KEY` | 空 | token 签名密钥，至少 32 字节 |
| `SEA_DATABASE_URL` / `SEA_REDIS_URL` | 本地 compose 地址 | 必需依赖 |
| `SEA_S3_ENDPOINT` / `SEA_S3_BUCKET` / `SEA_S3_ACCESS_KEY` / `SEA_S3_SECRET_KEY` | 本地 SeaweedFS | 对象存储 |
| `SEA_KAFKA_BROKERS` | `127.0.0.1:29092` | API 的可选 readiness 依赖（写请求仍原子进 Outbox） |
| `SEA_HTTP_ADDRESS` | `:8080` | 监听地址 |
| `SEA_OTEL_EXPORTER_OTLP_ENDPOINT` | 空 | 设置后导出 traces |
| `SEA_S3_DISABLE_DOWNLOAD_CACHE` | `false` | 签名 URL 缓存一键回退开关（A/B 用） |
| `SEA_RATE_IDENTITY_WRITE_RATE` / `..._BURST` | 见代码 | 身份写接口限流参数 |
| `SEA_MEDIA_QUEUED_ACTIVATION_INTERVAL` / `..._THRESHOLD` | `30s` / `2min` | 滞留转码任务兜底激活 |

compose 侧端口与凭据见 [.env.example](.env.example)（模板，真实 `.env` 不入库）。

## 已知限制

当前设计与实现上的已知限制（完整清单与修复记录见 [docs/backend-review.md](docs/backend-review.md)）：

- Access token 为无状态验签，refresh 家族撤销后已签发 token 在短 TTL（默认 15 分钟）内仍有效；备选方案为会话版本号进 JWT，取舍分析见评审文档。
- 推荐目前为候选集计算排序、无游标分页；热门/推荐 feed 的分页是已知改进点。
- 进程内签名 URL 缓存不跨实例共享，URL 撤销依赖短 TTL（剩余有效期 < 5s 不复用）。
- 模块间共享一个 PostgreSQL 实例（schema 隔离 + 边界测试），这是模块化单体的有意妥协，未来拆分路径沿 schema 所有权与事件边界进行（ADR 0001）。
- 性能数据来自单机压测环境，各组件同机运行，不构成生产容量或 SLA 声明。

## 文档索引

| 文档 | 内容 |
|---|---|
| [docs/architecture.md](docs/architecture.md) | 架构图、运行链路、一致性与降级边界 |
| [docs/backend-review.md](docs/backend-review.md) | 逐模块设计评审：亮点、风险与改进清单 |
| [docs/adr/](docs/adr/0001-modular-monolith.md) | 3 份关键架构决策记录（ADR） |
| [docs/performance/baseline.md](docs/performance/baseline.md) | 固定环境性能基线与 A/B 原始数据 |
| [docs/performance/benchmark-methodology.md](docs/performance/benchmark-methodology.md) | 可复现 k6 open-model benchmark 方法与归档格式 |
| [docs/runbooks/fault-drills.md](docs/runbooks/fault-drills.md) | 故障演练覆盖面与操作手册 |
| [docs/verification/final.md](docs/verification/final.md) | 全新环境最终验证报告 |
| [docs/demo.md](docs/demo.md) | 演示与验收脚本说明 |
| [api/openapi.json](api/openapi.json) / [docs/api-examples.md](docs/api-examples.md) | API 契约与调用示例 |

## License

[MIT](LICENSE)
