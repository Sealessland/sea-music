# 最终全新环境验证

- 执行日期：2026-07-13
- 命令：`make final-verify`
- 结果：通过（脚本退出码 0）

## 重建与迁移

终验先执行 `docker compose --profile observability down --volumes --remove-orphans`，删除本项目生成的数据卷，再从空白 PostgreSQL、Redis、SeaweedFS、Kafka、Prometheus、Tempo 和 Grafana volumes 启动。

- bootstrap 应用 15 个 migration
- 固定 seed `20260712` 基础 fixture 成功装载
- PostgreSQL、Redis、对象存储、Kafka、Prometheus、Grafana 健康检查通过；Collector/Tempo 的端到端写入和查询通过

## 测试与真实 E2E

`make verify` 重建 8 个测试数据库并通过全部 Go package 测试。正式 E2E 完成注册、登录、真实 MP4 S3 直传和校验、Outbox→Kafka→Inbox、ffprobe/ffmpeg、审核发布、签名 URL 播放探测、社交/发现、撤稿过滤、漂移修复、令牌重放撤销、限流和指标断言，输出 `verification complete`。

## 可观测性与故障恢复

- `make verify-observability` 通过；终验后 Tempo 查询到 `sea-music-api=20`、`sea-music-worker=20` 条 trace。
- TraceQL 查询 `object_store.presign_download` 命中公开视频详情 trace，其中两个签名 span 可见，实测该 trace 总时长 3ms。
- `make fault-drill` 的 Outbox backlog/ack 窗口/毒消息、热门重复事件和真实 ffmpeg Worker 中断恢复测试通过。

## 负载 smoke

固定 seed `20260713`，并发 8：

| 场景 | 请求 | 错误 | 吞吐 | p50 | p95 | p99 |
|---|---:|---:|---:|---:|---:|---:|
| 视频详情读 | 100 | 0 | 3016.5 RPS | 1.015ms | 16.808ms | 24.830ms |
| 突发点赞切换 | 100 | 0 | 926.9 RPS | 7.924ms | 19.493ms | 24.233ms |

完整 Outbox 恢复为 214ms；最终 `pending=0`、`publishing=0`、`failed=0`。负载后 SQL pool 为 8 open / 0 in-use / 8 idle，Redis pool timeout 为 0。原始结果是 `artifacts/performance/latest.json` 与 `latest.prom`。

本报告验证本地权威运行路径，不等同于生产环境 SLA；性能外推边界见 [基线报告](../performance/baseline.md)。
