# 演示与验收脚本

## 一键真实纵切

```sh
make bootstrap
make verify
```

`make verify` 不是 mock 演示：它注册与登录真实用户，生成真实 MP4，走 S3 直传及校验，等待 Outbox→Kafka→Inbox，运行 ffprobe/ffmpeg，审核发布，再用签名播放 URL 执行 ffprobe。随后验证评论/弹幕清洗、幂等点赞、热门 feed、撤稿过滤、计数漂移修复、token 重放撤销和 Redis 限流。

## 运维与性能证据

```sh
make verify-observability  # Collector 与 Tempo 中能查到 API/Worker traces
make fault-drill           # Outbox、Redis、重复投递、Worker 中断
make loadtest              # 固定 seed 的详情读、点赞突发、backlog 恢复
```

需要从空白本项目数据卷重放全部交付证据时运行 `make final-verify`。它会删除并重建 `sea-music` Compose 项目的本地生成数据，随后执行 bootstrap、测试/E2E、观测验证、故障演练和 100 请求负载 smoke。

演示时建议依次展示：`/readyz` 依赖状态、投稿状态审计、Outbox/Inbox 行、对象存储 rendition、Tempo 事件链 trace、Grafana RED/USE 仪表盘和 `artifacts/performance` 原始 JSON。
