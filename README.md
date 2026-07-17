# Sea Music

面向 Bilibili 类 UGC 视频社区核心业务的 Go 后端项目，采用模块化单体 API + 独立 Worker：覆盖身份鉴权、对象存储直传、真实媒体处理、审核发布、可靠事件、社交互动、内容发现、观测和故障恢复。

## 本地启动

要求：Go 1.26、Docker Compose（或兼容的 Podman Compose）、curl。

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

API 启动后，用户前台与接口同源提供：

- 前台首页：<http://127.0.0.1:8080/>
- 支持匿名热门流，以及注册/登录、个性推荐、关注流、播放详情、点赞、收藏、关注、评论和弹幕
- 本地 `8080` 被占用时可设置 `SEA_HTTP_ADDRESS=127.0.0.1:38080`

运行包含真实 PostgreSQL 集成测试和正式 API 健康请求的验证：

```sh
make verify
```

可选观测栈：

```sh
docker compose --profile observability up -d --wait
```

- Grafana: <http://127.0.0.1:33000>
- Prometheus: <http://127.0.0.1:39090>
- Tempo: <http://127.0.0.1:33200>
- OpenTelemetry gRPC/HTTP: `127.0.0.1:34317` / `127.0.0.1:34318`

运行包含正式 API、Worker、Collector 和 Tempo 落地查询的观测验证：

```sh
make verify-observability
```

其他可重放证据：

```sh
make fault-drill
make loadtest
```

## 项目资料

- [架构与运行链路](docs/architecture.md)
- [后端评审见解：亮点、风险与改进清单](docs/backend-review.md)
- [后端面试学习手册：八股、源码与项目拷打](docs/interview-study-guide.md)
- [交互式后端面试学习工作台](docs/interview-study-guide.html)
- [OpenAPI 契约](api/openapi.json) 与 [调用示例](docs/api-examples.md)
- [一键演示](docs/demo.md) 与 [故障 runbook](docs/runbooks/fault-drills.md)
- [性能基线和 A/B 原始证据](docs/performance/baseline.md)
- [可复现 open-model Benchmark 方法](docs/performance/benchmark-methodology.md)
- [全新环境最终验证](docs/verification/final.md)
- [可证实成果摘要](docs/resume-evidence.md)
