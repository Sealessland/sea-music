# 消息队列对比

Kafka、RocketMQ 与 JetStream 的可比较数据由 `make queue-benchmark` 生成：每个 broker 运行相同数量的 `burst-like-toggle` 请求、并发和固定 seed 数据集；记录写路径吞吐、P95/P99、Outbox 全状态恢复时间、错误数及对应 Prometheus 快照。每次运行保存在 `artifacts/queue-benchmarks/<run-id>/`，含 JSON、`SHA256SUMS` 和环境清单；GitHub Actions 保存 14 天 artifact 并在 Job Summary 中显示中位数。

当前三方结果（2026-07-21，[CI run 29822812646](https://github.com/Sealessland/sea-music/actions/runs/29822812646)，每组 3 次中位数；固定 500 请求、并发 16）：Kafka `1456.5 RPS / p95 27.8ms / p99 37.3ms / Outbox 恢复 0.436s`；RocketMQ `783.2 RPS / p95 61.3ms / p99 101.2ms / Outbox 恢复 2.243s`；JetStream `1294.7 RPS / p95 32.8ms / p99 49.6ms / Outbox 恢复 0.077s`。每个 broker 共 3,000 个总请求，0 错误。证据 artifact `queue-benchmark-29822812646-1` 的 SHA-256 为 `58d87af6ee256cba6e8c7809d5b55029e1e24ae71391d1d1e293dd9175393c4f`。Kafka 使用 bootstrap server，RocketMQ 使用 Proxy（`SEA_ROCKETMQ_ENDPOINT`），JetStream 使用单节点 file-backed stream；协议、队列模型和 runner 噪声不同，结果仅适用于同一 runner、提交和参数的成对回归，不外推为生产 SLA。


# 固定环境性能基线与优化

> 历史口径说明：本页的 `2998 -> 3645 RPS（+21.6%）` 来自固定并发、
> 完成一个请求后再发送下一个请求的 closed-model 微基准。百分比计算本身正确，
> 但它不能解释为服务的可持续容量，尾延迟也可能受到 coordinated omission 影响。
> 新的正式测量方法见 [可复现 HTTP Benchmark](benchmark-methodology.md)：使用固定版本
> k6、`constant-arrival-rate`、缓存 A/B 交替顺序、资源约束、SLA 阈值与不可变归档。

## 环境

- 日期：2026-07-13；Linux 7.1.3-2-cachyos x86_64
- CPU：Intel Core i7-1185G7，4 核 / 8 线程；内存 31 GiB
- Go 1.26；并发 16；每场景 500 请求
- 数据集 seed `20260713`：1,000 用户、500 视频、5,000 关注、4,000 点赞、1,500 收藏、1,000 评论、1,500 弹幕
- PostgreSQL、Redis、SeaweedFS、Kafka 与 API/Worker 在同一宿主机运行；每组使用同一源码构建，唯一变量为 `SEA_S3_DISABLE_DOWNLOAD_CACHE`

## 原始结果

详情读列依次为 `RPS / p50 / p95 / p99`，延迟单位毫秒。

| 组 | Run 1 | Run 2 | Run 3 | 中位数 |
|---|---:|---:|---:|---:|
| 禁用缓存 | 2393 / 4.147 / 28.792 / 38.082 | 3414 / 2.587 / 20.930 / 38.981 | 2998 / 2.814 / 21.566 / 52.315 | 2998 / 2.814 / 21.566 / 38.981 |
| 启用缓存 | 3657 / 2.341 / 20.866 / 34.287 | 1631 / 5.597 / 41.221 / 76.596 | 3645 / 2.664 / 20.349 / 29.879 | 3645 / 2.664 / 20.866 / 34.287 |

两组共 3,000 次详情请求均为 0 错误。完整 A/B 样本保存在 `artifacts/performance/{no-cache,cache}-run{1,2,3}.json`。这些早期文件中的 `backlog_recovery_ms` 只等待了 `pending=0`，没有包含仍持有租约的 `publishing`，因此不作为恢复结论使用；终验发现后已增加回归测试并修正。

修正后的 `corrected-backlog-recovery.{json,prom}` 同时等待 `pending + publishing == 0`，并要求 `failed == 0`：本次 500 个突发关系请求完整恢复为 976ms，最终三种 Outbox 状态均为 0。负载后 SQL 池为 10 open / 0 in-use / 10 idle（配置上限 20），Redis pool timeout 为 0，未见依赖池饱和。

## 瓶颈与回归驱动优化

公开视频详情每次都生成 playback 和 cover 两个 SigV4 URL。SQL/Redis USE 指标未显示依赖池耗尽，而详情 trace 的本地路径包含两个 `object_store.presign_download` span，因此将重复签名确定为可隔离的 CPU/分配热点。先增加缓存容量上限回归测试，再实现过期感知、最多 10,000 项的缓存；trace 用 `cache.hit` 标明命中。

按三次中位数，优化使详情吞吐从 2998 提升到 3645 RPS（+21.6%），p50 降低 5.3%，p95 降低 3.2%，p99 降低 12.0%。这是本机短请求微基准，不外推为分布式生产容量。

## 结论边界

- 缓存组 Run 2 有明显宿主抖动；所以只使用预先约定的三次中位数，并保留全部原始值。
- 压测未包含公网延迟、TLS、跨可用区对象存储或多实例缓存冷启动。
- 点赞场景和 backlog 指标的组间差异不归因于 URL 缓存。
- URL 在剩余有效期小于 5 秒时不会复用；缓存是有界进程内优化，不改变授权语义。
- `+21.6%` 只保留为历史优化线索，不再作为容量结论；新的面试表达应优先说明
  benchmark 设计、固定 offered load 下的延迟/错误/dropped iterations，以及实验边界。
