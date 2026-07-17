# API 示例

以下假设 API 位于 `http://127.0.0.1:8080`，并已将登录响应中的 access token 放入 `TOKEN`。

```sh
curl -sS -H 'Content-Type: application/json' \
  -d '{"username":"creator_01","email":"creator@example.test","password":"a secure demo password"}' \
  http://127.0.0.1:8080/api/v1/users

curl -sS -H 'Content-Type: application/json' \
  -d '{"identity":"creator@example.test","password":"a secure demo password"}' \
  http://127.0.0.1:8080/api/v1/sessions
```

创建投稿后，请求上传授权；客户端必须按授权请求携带相同的 `Content-Type` 和 `x-amz-meta-sha256`，上传完成再 finalize。

```sh
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"一次真实投稿","description":"direct upload + ffmpeg"}' \
  http://127.0.0.1:8080/api/v1/videos

curl -sS -X PUT -H "Authorization: Bearer $TOKEN" \
  http://127.0.0.1:8080/api/v1/videos/$VIDEO_ID/like

curl -sS -H "Authorization: Bearer $TOKEN" \
  'http://127.0.0.1:8080/api/v1/feed/recommendations?limit=20'
```

游标是不透明字符串，只能原样传回。错误统一为 `{"error":{"code","message","details?"},"request_id"}`；限流响应额外包含 `Retry-After`。完整机器可读契约见 [`api/openapi.json`](../api/openapi.json)。
