#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

export GOCACHE="${GOCACHE:-/tmp/sea-music-go-cache}"
export GOMODCACHE="${GOMODCACHE:-/tmp/sea-music-go-mod}"
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
export SEA_DATABASE_ADMIN_URL="${SEA_DATABASE_ADMIN_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/postgres?sslmode=disable}"
export SEA_MIGRATION_TEST_DATABASE_URL="${SEA_MIGRATION_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_migration_test?sslmode=disable}"
export SEA_FIXTURE_TEST_DATABASE_URL="${SEA_FIXTURE_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_fixture_test?sslmode=disable}"
export SEA_IDENTITY_TEST_DATABASE_URL="${SEA_IDENTITY_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_identity_test?sslmode=disable}"
export SEA_API_TEST_DATABASE_URL="${SEA_API_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_api_test?sslmode=disable}"
export SEA_VIDEO_TEST_DATABASE_URL="${SEA_VIDEO_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_video_test?sslmode=disable}"
export SEA_EVENTS_TEST_DATABASE_URL="${SEA_EVENTS_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_events_test?sslmode=disable}"
export SEA_SOCIAL_TEST_DATABASE_URL="${SEA_SOCIAL_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_social_test?sslmode=disable}"
export SEA_DISCOVERY_TEST_DATABASE_URL="${SEA_DISCOVERY_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_discovery_test?sslmode=disable}"
export SEA_MODERATION_TEST_DATABASE_URL="${SEA_MODERATION_TEST_DATABASE_URL:-postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music_moderation_test?sslmode=disable}"
export SEA_EVENTS_TEST_BROKER="${SEA_EVENTS_TEST_BROKER:-127.0.0.1:29092}"
export SEA_VIDEO_TEST_S3_ENDPOINT="${SEA_VIDEO_TEST_S3_ENDPOINT:-http://127.0.0.1:28333}"
export SEA_REDIS_TEST_URL="${SEA_REDIS_TEST_URL:-redis://:local-redis-password@127.0.0.1:26379/15}"
export SEA_API_REDIS_URL="${SEA_API_REDIS_URL:-redis://:local-redis-password@127.0.0.1:26379/14}"

docker compose up -d --wait postgres redis object-store broker

SEA_TEST_DATABASE_NAME=sea_music_migration_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_fixture_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_identity_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_api_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_video_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_events_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_social_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_discovery_test go run -buildvcs=false ./cmd/testdb
SEA_TEST_DATABASE_NAME=sea_music_moderation_test go run -buildvcs=false ./cmd/testdb
SEA_DATABASE_URL="$SEA_API_TEST_DATABASE_URL" go run -buildvcs=false ./cmd/migrate up
docker compose exec -T redis sh -c 'redis-cli -a "$REDIS_PASSWORD" -n 14 FLUSHDB >/dev/null'

go vet ./...
go test -race -count=1 ./...

API_BINARY=/tmp/sea-music-api-verify
API_LOG=/tmp/sea-music-api-verify.log
go build -buildvcs=false -o "$API_BINARY" ./cmd/api
WORKER_BINARY=/tmp/sea-music-worker-verify
WORKER_LOG=/tmp/sea-music-worker-verify.log
go build -buildvcs=false -o "$WORKER_BINARY" ./cmd/worker
MODERATION_BINARY=/tmp/sea-music-moderation-agent-verify
MODERATION_LOG=/tmp/sea-music-moderation-agent-verify.log
go build -buildvcs=false -o "$MODERATION_BINARY" ./cmd/moderation-agent

SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL="$SEA_API_TEST_DATABASE_URL" \
SEA_REDIS_URL="$SEA_API_REDIS_URL" \
SEA_RATE_IDENTITY_WRITE_RATE=0.01 \
SEA_RATE_IDENTITY_WRITE_BURST=10 \
SEA_HTTP_ADDRESS="${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}" \
"$API_BINARY" >"$API_LOG" 2>&1 &
API_PID=$!
WORKER_PID=
MODERATION_PID=

cleanup() {
    if [ -n "$WORKER_PID" ] && kill -0 "$WORKER_PID" 2>/dev/null; then
        kill -TERM "$WORKER_PID"
        wait "$WORKER_PID"
    fi
    if [ -n "$MODERATION_PID" ] && kill -0 "$MODERATION_PID" 2>/dev/null; then
        kill -TERM "$MODERATION_PID"
        wait "$MODERATION_PID"
    fi
    if kill -0 "$API_PID" 2>/dev/null; then
        kill -TERM "$API_PID"
        wait "$API_PID"
    fi
}
trap cleanup EXIT INT TERM

attempt=0
until curl --fail --silent --show-error "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/livez" >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 30 ]; then
        echo "API did not become live; log follows" >&2
        sed -n '1,200p' "$API_LOG" >&2
        exit 1
    fi
    sleep 0.2
done

curl --fail --silent --show-error "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/readyz" >/dev/null

REGISTER_BODY=/tmp/sea-music-register-response.json
REGISTER_STATUS=$(curl --silent --show-error --output "$REGISTER_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"username":"verify_creator","email":"verify@example.com","password":"correct horse battery staple"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/users")
if [ "$REGISTER_STATUS" != "201" ]; then
    echo "registration returned HTTP $REGISTER_STATUS" >&2
    sed -n '1,80p' "$REGISTER_BODY" >&2
    exit 1
fi
if ! rg --quiet '"username":"verify_creator"' "$REGISTER_BODY"; then
    echo "registration response is missing the normalized user" >&2
    sed -n '1,80p' "$REGISTER_BODY" >&2
    exit 1
fi
if rg --quiet 'password|password_hash|correct horse' "$REGISTER_BODY"; then
    echo "registration response leaked credential material" >&2
    exit 1
fi

DUPLICATE_BODY=/tmp/sea-music-register-duplicate.json
DUPLICATE_STATUS=$(curl --silent --show-error --output "$DUPLICATE_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"username":"verify_creator","email":"other@example.com","password":"correct horse battery staple"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/users")
if [ "$DUPLICATE_STATUS" != "409" ]; then
    echo "duplicate registration returned HTTP $DUPLICATE_STATUS" >&2
    sed -n '1,80p' "$DUPLICATE_BODY" >&2
    exit 1
fi
if rg --quiet 'users_username_unique|password_hash|correct horse' "$DUPLICATE_BODY"; then
    echo "duplicate response leaked database or credential details" >&2
    exit 1
fi

LOGIN_BODY=/tmp/sea-music-login-response.json
LOGIN_STATUS=$(curl --silent --show-error --output "$LOGIN_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"identity":"VERIFY@example.com","password":"correct horse battery staple"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions")
if [ "$LOGIN_STATUS" != "200" ]; then
    echo "login returned HTTP $LOGIN_STATUS" >&2
    sed -n '1,80p' "$LOGIN_BODY" >&2
    exit 1
fi
ACCESS_TOKEN=$(jq --exit-status --raw-output '.access_token' "$LOGIN_BODY")
REFRESH_TOKEN=$(jq --exit-status --raw-output '.refresh_token' "$LOGIN_BODY")
if [ -z "$ACCESS_TOKEN" ] || [ -z "$REFRESH_TOKEN" ]; then
    echo "login did not return both tokens" >&2
    exit 1
fi
if rg --quiet 'password_hash|correct horse' "$LOGIN_BODY"; then
    echo "login response leaked credential material" >&2
    exit 1
fi

UNAUTHENTICATED_ME_STATUS=$(curl --silent --show-error --output /tmp/sea-music-me-unauthenticated.json --write-out '%{http_code}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/me")
if [ "$UNAUTHENTICATED_ME_STATUS" != "401" ]; then
    echo "unauthenticated /me returned HTTP $UNAUTHENTICATED_ME_STATUS" >&2
    exit 1
fi

ME_BODY=/tmp/sea-music-me-response.json
ME_STATUS=$(curl --silent --show-error --output "$ME_BODY" --write-out '%{http_code}' \
    --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/me")
if [ "$ME_STATUS" != "200" ] || ! rg --quiet '"username":"verify_creator"' "$ME_BODY"; then
    echo "authenticated /me returned HTTP $ME_STATUS or wrong identity" >&2
    sed -n '1,80p' "$ME_BODY" >&2
    exit 1
fi

VIDEO_BODY=/tmp/sea-music-video-response.json
VIDEO_STATUS=$(curl --silent --show-error --output "$VIDEO_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header "Authorization: Bearer $ACCESS_TOKEN" \
    --data '{"title":"Verified direct upload","description":"real API to SeaweedFS path"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos")
if [ "$VIDEO_STATUS" != "201" ]; then
    echo "video draft returned HTTP $VIDEO_STATUS" >&2
    sed -n '1,80p' "$VIDEO_BODY" >&2
    exit 1
fi
VIDEO_ID=$(jq --exit-status --raw-output '.video.id' "$VIDEO_BODY")
SOURCE_FILE=/tmp/sea-music-verify-source.mp4
ffmpeg -hide_banner -loglevel error -f lavfi -i 'testsrc=size=320x180:rate=24' \
    -t 1 -pix_fmt yuv420p -c:v libx264 -y "$SOURCE_FILE"
SOURCE_SIZE=$(wc -c <"$SOURCE_FILE" | tr -d ' ')
SOURCE_CHECKSUM=$(sha256sum "$SOURCE_FILE" | awk '{print $1}')
UPLOAD_JSON=$(jq --null-input --compact-output \
    --argjson size "$SOURCE_SIZE" --arg checksum "$SOURCE_CHECKSUM" \
    '{size_bytes:$size,content_type:"video/mp4",checksum_sha256:$checksum}')
UPLOAD_BODY=/tmp/sea-music-upload-grant.json
UPLOAD_STATUS=$(curl --silent --show-error --output "$UPLOAD_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header "Authorization: Bearer $ACCESS_TOKEN" \
    --data "$UPLOAD_JSON" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/uploads")
if [ "$UPLOAD_STATUS" != "201" ]; then
    echo "upload grant returned HTTP $UPLOAD_STATUS" >&2
    sed -n '1,80p' "$UPLOAD_BODY" >&2
    exit 1
fi
UPLOAD_URL=$(jq --exit-status --raw-output '.upload.upload_url' "$UPLOAD_BODY")
PUT_STATUS=$(curl --silent --show-error --output /tmp/sea-music-upload-put.json --write-out '%{http_code}' \
    --request PUT --header 'Content-Type: video/mp4' --header "x-amz-meta-sha256: $SOURCE_CHECKSUM" \
    --data-binary "@$SOURCE_FILE" "$UPLOAD_URL")
if [ "$PUT_STATUS" != "200" ]; then
    echo "signed object upload returned HTTP $PUT_STATUS" >&2
    sed -n '1,80p' /tmp/sea-music-upload-put.json >&2
    exit 1
fi
FINALIZE_BODY=/tmp/sea-music-finalize-response.json
FINALIZE_STATUS=$(curl --silent --show-error --output "$FINALIZE_BODY" --write-out '%{http_code}' \
    --request POST --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/finalize")
if [ "$FINALIZE_STATUS" != "200" ] || ! jq --exit-status '.video.state == "uploaded" and (.job_id | length > 0)' "$FINALIZE_BODY" >/dev/null; then
    echo "upload finalize returned HTTP $FINALIZE_STATUS or invalid result" >&2
    sed -n '1,80p' "$FINALIZE_BODY" >&2
    exit 1
fi
FIRST_JOB_ID=$(jq --exit-status --raw-output '.job_id' "$FINALIZE_BODY")
SECOND_JOB_ID=$(curl --fail --silent --show-error --request POST \
    --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/finalize" | jq --exit-status --raw-output '.job_id')
if [ "$FIRST_JOB_ID" != "$SECOND_JOB_ID" ]; then
    echo "repeated finalize created a different processing job" >&2
    exit 1
fi

SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL="$SEA_API_TEST_DATABASE_URL" \
SEA_MODERATION_GRPC_ADDRESS=127.0.0.1:39091 \
SEA_MODERATION_METRICS_ADDRESS=127.0.0.1:39092 \
"$MODERATION_BINARY" >"$MODERATION_LOG" 2>&1 &
MODERATION_PID=$!
MODERATION_READY_ATTEMPT=0
until curl --fail --silent --show-error http://127.0.0.1:39092/readyz >/dev/null; do
    MODERATION_READY_ATTEMPT=$((MODERATION_READY_ATTEMPT + 1))
    if [ "$MODERATION_READY_ATTEMPT" -ge 30 ]; then
        echo "moderation agent did not become ready; log follows" >&2
        sed -n '1,200p' "$MODERATION_LOG" >&2
        exit 1
    fi
    sleep 0.2
done

SEA_AUTH_TOKEN_KEY=0123456789abcdef0123456789abcdef \
SEA_DATABASE_URL="$SEA_API_TEST_DATABASE_URL" \
SEA_REDIS_URL="$SEA_API_REDIS_URL" \
SEA_COUNTER_RECONCILE_INTERVAL=250ms \
SEA_MODERATION_AGENT_ADDRESS=127.0.0.1:39091 \
SEA_MODERATION_POLL_INTERVAL=100ms \
"$WORKER_BINARY" >"$WORKER_LOG" 2>&1 &
WORKER_PID=$!
PROCESS_ATTEMPT=0
VIDEO_STATE=
until [ "$VIDEO_STATE" = "review" ]; do
    PROCESS_ATTEMPT=$((PROCESS_ATTEMPT + 1))
    if [ "$PROCESS_ATTEMPT" -ge 40 ]; then
        echo "worker did not move video to review; worker log follows" >&2
        sed -n '1,200p' "$WORKER_LOG" >&2
        exit 1
    fi
    sleep 0.25
    VIDEO_STATE=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
        "SELECT state FROM video.videos WHERE id = '$VIDEO_ID'" | tr -d '[:space:]')
done
RENDITION_COUNT=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
    "SELECT count(*) FROM video.renditions r JOIN video.source_assets a ON a.id = r.source_asset_id WHERE a.video_id = '$VIDEO_ID' AND r.status = 'ready'" | tr -d '[:space:]')
if [ "$RENDITION_COUNT" != "2" ]; then
    echo "worker produced $RENDITION_COUNT ready renditions, want 2" >&2
    exit 1
fi
MODERATION_ATTEMPT=0
MODERATION_STATE=
until [ "$MODERATION_STATE" = "completed" ]; do
    MODERATION_ATTEMPT=$((MODERATION_ATTEMPT + 1))
    if [ "$MODERATION_ATTEMPT" -ge 80 ]; then
        echo "agent moderation did not complete; agent and worker logs follow" >&2
        sed -n '1,200p' "$MODERATION_LOG" >&2
        sed -n '1,200p' "$WORKER_LOG" >&2
        exit 1
    fi
    sleep 0.25
    MODERATION_STATE=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
        "SELECT state FROM moderation.dispatch_jobs WHERE video_id = '$VIDEO_ID'" | tr -d '[:space:]')
done
MODERATION_VERDICT=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
    "SELECT result->>'verdict' FROM moderation.dispatch_jobs WHERE video_id = '$VIDEO_ID'" | tr -d '[:space:]')
if [ "$MODERATION_VERDICT" != "escalate" ]; then
    echo "disabled-provider shadow verdict was $MODERATION_VERDICT, want escalate" >&2
    exit 1
fi
curl --fail --silent --show-error http://127.0.0.1:39092/metrics > /tmp/sea-music-moderation-metrics.txt
if ! rg --quiet 'grpc_server_handled_total\{.*grpc_code="OK".*\} [1-9]' /tmp/sea-music-moderation-metrics.txt; then
    echo "moderation gRPC success counter was not collected" >&2
    sed -n '1,160p' /tmp/sea-music-moderation-metrics.txt >&2
    exit 1
fi
docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -v ON_ERROR_STOP=1 -c \
    "UPDATE identity.users SET role = 'moderator' WHERE username = 'verify_creator'" >/dev/null
MODERATOR_LOGIN_BODY=/tmp/sea-music-moderator-login.json
MODERATOR_LOGIN_STATUS=$(curl --silent --show-error --output "$MODERATOR_LOGIN_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"identity":"verify@example.com","password":"correct horse battery staple"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions")
if [ "$MODERATOR_LOGIN_STATUS" != "200" ]; then
    echo "moderator relogin returned HTTP $MODERATOR_LOGIN_STATUS" >&2
    exit 1
fi
MODERATOR_ACCESS_TOKEN=$(jq --exit-status --raw-output '.access_token' "$MODERATOR_LOGIN_BODY")
REVIEW_VERSION=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
    "SELECT version FROM video.videos WHERE id = '$VIDEO_ID'" | tr -d '[:space:]')
REVIEW_BODY=/tmp/sea-music-review-response.json
REVIEW_STATUS=$(curl --silent --show-error --output "$REVIEW_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header "Authorization: Bearer $MODERATOR_ACCESS_TOKEN" \
    --data "{\"expected_version\":$REVIEW_VERSION,\"approved\":true,\"reason\":\"automated policy passed\"}" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/review")
if [ "$REVIEW_STATUS" != "200" ] || ! jq --exit-status '.video.state == "published"' "$REVIEW_BODY" >/dev/null; then
    echo "moderation publish returned HTTP $REVIEW_STATUS" >&2
    sed -n '1,80p' "$REVIEW_BODY" >&2
    exit 1
fi
PUBLISHED_VERSION=$(jq --exit-status --raw-output '.video.version' "$REVIEW_BODY")
PUBLIC_BODY=/tmp/sea-music-public-video.json
PUBLIC_STATUS=$(curl --silent --show-error --output "$PUBLIC_BODY" --write-out '%{http_code}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID")
if [ "$PUBLIC_STATUS" != "200" ]; then
    echo "public video returned HTTP $PUBLIC_STATUS" >&2
    sed -n '1,80p' "$PUBLIC_BODY" >&2
    exit 1
fi
PUBLIC_PLAYBACK_URL=$(jq --exit-status --raw-output '.video.playback_url' "$PUBLIC_BODY")
curl --fail --silent --show-error "$PUBLIC_PLAYBACK_URL" --output /tmp/sea-music-public-playback.mp4
ffprobe -v error -select_streams v:0 -show_entries stream=codec_name \
    -of default=nw=1:nk=1 /tmp/sea-music-public-playback.mp4 | rg --quiet '^h264$'
COMMENT_BODY=/tmp/sea-music-comment-response.json
COMMENT_STATUS=$(curl --silent --show-error --output "$COMMENT_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' --header "Authorization: Bearer $ACCESS_TOKEN" \
    --data '{"body":"verified top-level comment"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/comments")
if [ "$COMMENT_STATUS" != "201" ]; then
    echo "comment creation returned HTTP $COMMENT_STATUS" >&2
    exit 1
fi
curl --fail --silent --show-error \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/comments?limit=20" |
    jq --exit-status '.items[0].body == "verified top-level comment"' >/dev/null
DANMAKU_BODY=/tmp/sea-music-danmaku-response.json
DANMAKU_STATUS=$(curl --silent --show-error --output "$DANMAKU_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' --header "Authorization: Bearer $ACCESS_TOKEN" \
    --data '{"position_ms":500,"body":"<b>verified danmaku</b>"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/danmaku")
if [ "$DANMAKU_STATUS" != "201" ] || rg --quiet '<b>' "$DANMAKU_BODY"; then
    echo "danmaku creation was rejected or not sanitized; HTTP $DANMAKU_STATUS" >&2
    exit 1
fi
curl --fail --silent --show-error \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/danmaku?start_ms=0&end_ms=1000&limit=20" |
    jq --exit-status '.items | length == 1' >/dev/null
FIRST_LIKE=$(curl --fail --silent --show-error --request PUT --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/like")
SECOND_LIKE=$(curl --fail --silent --show-error --request PUT --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/like")
if ! printf '%s' "$FIRST_LIKE" | jq --exit-status '.changed == true and .exists == true' >/dev/null ||
   ! printf '%s' "$SECOND_LIKE" | jq --exit-status '.changed == false and .exists == true' >/dev/null; then
    echo "repeated like was not idempotent" >&2
    exit 1
fi
FIRST_UNLIKE=$(curl --fail --silent --show-error --request DELETE --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/like")
SECOND_UNLIKE=$(curl --fail --silent --show-error --request DELETE --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/like")
if ! printf '%s' "$FIRST_UNLIKE" | jq --exit-status '.changed == true and .exists == false' >/dev/null ||
   ! printf '%s' "$SECOND_UNLIKE" | jq --exit-status '.changed == false and .exists == false' >/dev/null; then
    echo "repeated unlike was not idempotent" >&2
    exit 1
fi
HOT_ATTEMPT=0
HOT_ITEMS=0
until [ "$HOT_ITEMS" -ge 1 ]; do
    HOT_ATTEMPT=$((HOT_ATTEMPT + 1))
    if [ "$HOT_ATTEMPT" -ge 40 ]; then
        echo "hot ranking consumer did not expose the engaged video" >&2
        exit 1
    fi
    sleep 0.25
    HOT_ITEMS=$(curl --fail --silent --show-error --header "Authorization: Bearer $ACCESS_TOKEN" \
        "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/feed/hot?limit=10" | jq --exit-status '.items | length')
done
curl --fail --silent --show-error --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/feed/recommendations?limit=10" >/dev/null
curl --fail --silent --show-error --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/feed/following?limit=10" >/dev/null
WITHDRAW_BODY=/tmp/sea-music-withdraw-response.json
WITHDRAW_STATUS=$(curl --silent --show-error --output "$WITHDRAW_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --header "Authorization: Bearer $ACCESS_TOKEN" \
    --data "{\"expected_version\":$PUBLISHED_VERSION,\"reason\":\"creator requested withdrawal\"}" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID/withdraw")
if [ "$WITHDRAW_STATUS" != "200" ] || ! jq --exit-status '.video.state == "withdrawn"' "$WITHDRAW_BODY" >/dev/null; then
    echo "video withdrawal returned HTTP $WITHDRAW_STATUS" >&2
    exit 1
fi
WITHDRAWN_PUBLIC_STATUS=$(curl --silent --show-error --output /tmp/sea-music-withdrawn-public.json --write-out '%{http_code}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/videos/$VIDEO_ID")
if [ "$WITHDRAWN_PUBLIC_STATUS" != "404" ]; then
    echo "withdrawn video remained publicly visible; HTTP $WITHDRAWN_PUBLIC_STATUS" >&2
    exit 1
fi
WITHDRAWN_HOT_ITEMS=$(curl --fail --silent --show-error --header "Authorization: Bearer $ACCESS_TOKEN" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/feed/hot?limit=10" | jq --exit-status '.items | length')
if [ "$WITHDRAWN_HOT_ITEMS" != "0" ]; then
    echo "withdrawn video remained visible in cached hot feed" >&2
    exit 1
fi
EVENT_CHAIN_ATTEMPT=0
EVENT_CHAIN_TYPES=0
until [ "$EVENT_CHAIN_TYPES" = "7" ]; do
    EVENT_CHAIN_ATTEMPT=$((EVENT_CHAIN_ATTEMPT + 1))
    if [ "$EVENT_CHAIN_ATTEMPT" -ge 40 ]; then
        echo "event chain did not publish and consume every expected video/social/moderation event type" >&2
        sed -n '1,200p' "$WORKER_LOG" >&2
        exit 1
    fi
    sleep 0.25
    EVENT_CHAIN_TYPES=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
        "WITH expected(event_type, event_count) AS (VALUES ('video.source_finalized',1),('video.ready_for_moderation',1),('video.published',1),('video.withdrawn',1),('social.like.changed',2),('social.comment.created',1),('social.danmaku.created',1)) SELECT count(*) FROM expected e WHERE (SELECT count(*) FROM eventing.outbox o JOIN eventing.inbox i ON i.event_id = o.id AND i.consumer_name = 'media-job-activation' WHERE o.event_type = e.event_type AND o.state = 'published') = e.event_count" | tr -d '[:space:]')
done
COUNTER_ATTEMPT=0
COUNTER_RESULT=
until [ "$COUNTER_RESULT" = "2:0:1:1" ]; do
    COUNTER_ATTEMPT=$((COUNTER_ATTEMPT + 1))
    if [ "$COUNTER_ATTEMPT" -ge 40 ]; then
        echo "social counter projection did not consume like/unlike exactly once" >&2
        exit 1
    fi
    sleep 0.25
    COUNTER_RESULT=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
        "SELECT (SELECT count(*) FROM eventing.inbox i JOIN eventing.outbox o ON o.id = i.event_id WHERE i.consumer_name = 'social-counters' AND o.event_type = 'social.like.changed' AND o.aggregate_id = '$VIDEO_ID') || ':' || COALESCE((SELECT likes || ':' || comments || ':' || danmaku FROM social.video_counters WHERE video_id = '$VIDEO_ID'), '-1:-1:-1')" | tr -d '[:space:]')
done
docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -v ON_ERROR_STOP=1 -c \
    "UPDATE social.video_counters SET likes = 99 WHERE video_id = '$VIDEO_ID'" >/dev/null
RECONCILE_ATTEMPT=0
RECONCILED=
until [ "$RECONCILED" = "0:1" ]; do
    RECONCILE_ATTEMPT=$((RECONCILE_ATTEMPT + 1))
    if [ "$RECONCILE_ATTEMPT" -ge 40 ]; then
        echo "counter reconciliation did not repair deliberate drift" >&2
        exit 1
    fi
    sleep 0.25
    RECONCILED=$(docker compose exec -T postgres psql -U sea_music -d sea_music_api_test -tAc \
        "SELECT COALESCE((SELECT likes FROM social.video_counters WHERE video_id = '$VIDEO_ID'), -1) || ':' || ((SELECT count(*) FROM social.counter_reconciliations WHERE video_id = '$VIDEO_ID') > 0)::int" | tr -d '[:space:]')
done
kill -TERM "$WORKER_PID"
wait "$WORKER_PID"
WORKER_PID=

TAMPERED_ME_STATUS=$(curl --silent --show-error --output /tmp/sea-music-me-tampered.json --write-out '%{http_code}' \
    --header "Authorization: Bearer ${ACCESS_TOKEN}tampered" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/me")
if [ "$TAMPERED_ME_STATUS" != "401" ]; then
    echo "tampered access token returned HTTP $TAMPERED_ME_STATUS" >&2
    exit 1
fi

BAD_LOGIN_BODY=/tmp/sea-music-bad-login.json
BAD_LOGIN_STATUS=$(curl --silent --show-error --output "$BAD_LOGIN_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data '{"identity":"verify@example.com","password":"incorrect password"}' \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions")
if [ "$BAD_LOGIN_STATUS" != "401" ] || rg --quiet 'password_hash|identity.users' "$BAD_LOGIN_BODY"; then
    echo "invalid login response is unsafe or returned HTTP $BAD_LOGIN_STATUS" >&2
    exit 1
fi

REFRESH_BODY=/tmp/sea-music-refresh-response.json
REFRESH_STATUS=$(curl --silent --show-error --output "$REFRESH_BODY" --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data "{\"refresh_token\":\"$REFRESH_TOKEN\"}" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions/refresh")
if [ "$REFRESH_STATUS" != "200" ]; then
    echo "refresh returned HTTP $REFRESH_STATUS" >&2
    sed -n '1,80p' "$REFRESH_BODY" >&2
    exit 1
fi
ROTATED_REFRESH_TOKEN=$(jq --exit-status --raw-output '.refresh_token' "$REFRESH_BODY")
if [ "$ROTATED_REFRESH_TOKEN" = "$REFRESH_TOKEN" ]; then
    echo "refresh token was not rotated" >&2
    exit 1
fi

REPLAY_STATUS=$(curl --silent --show-error --output /tmp/sea-music-refresh-replay.json --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data "{\"refresh_token\":\"$REFRESH_TOKEN\"}" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions/refresh")
if [ "$REPLAY_STATUS" != "401" ]; then
    echo "replayed refresh returned HTTP $REPLAY_STATUS" >&2
    exit 1
fi

REVOKED_STATUS=$(curl --silent --show-error --output /tmp/sea-music-refresh-revoked.json --write-out '%{http_code}' \
    --header 'Content-Type: application/json' \
    --data "{\"refresh_token\":\"$ROTATED_REFRESH_TOKEN\"}" \
    "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions/refresh")
if [ "$REVOKED_STATUS" != "401" ]; then
    echo "replacement token remained usable after family replay; HTTP $REVOKED_STATUS" >&2
    exit 1
fi

RATE_STATUS=0
RATE_ATTEMPT=0
while [ "$RATE_ATTEMPT" -lt 20 ]; do
    RATE_ATTEMPT=$((RATE_ATTEMPT + 1))
    RATE_STATUS=$(curl --silent --show-error --output /tmp/sea-music-rate-body.json --dump-header /tmp/sea-music-rate-headers.txt --write-out '%{http_code}' \
        --header 'Content-Type: application/json' \
        --data '{}' \
        "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/api/v1/sessions")
    if [ "$RATE_STATUS" = "429" ]; then
        break
    fi
done
if [ "$RATE_STATUS" != "429" ] || ! rg --ignore-case --quiet '^Retry-After: [1-9][0-9]*' /tmp/sea-music-rate-headers.txt; then
    echo "identity write rate limit did not return 429 with Retry-After" >&2
    sed -n '1,80p' /tmp/sea-music-rate-headers.txt >&2
    exit 1
fi

curl --fail --silent --show-error "http://${SEA_VERIFY_HTTP_ADDRESS:-127.0.0.1:38081}/metrics" >/tmp/sea-music-metrics.txt
if ! rg --quiet 'sea_music_rate_limit_rejected_total\{class="identity_write"\} [1-9][0-9]*' /tmp/sea-music-metrics.txt; then
    echo "rate limit rejection metric was not recorded" >&2
    sed -n '1,120p' /tmp/sea-music-metrics.txt >&2
    exit 1
fi
if ! rg --quiet 'sea_music_outbox_events\{state="pending"\} [0-9]+' /tmp/sea-music-metrics.txt; then
    echo "outbox backlog metric was not exposed" >&2
    exit 1
fi
if ! rg --quiet 'sea_music_http_requests_total\{method="GET",route="/api/v1/me",status_class="2xx"\} [1-9][0-9]*' /tmp/sea-music-metrics.txt; then
    echo "bounded-route HTTP RED metric was not recorded" >&2
    exit 1
fi
if ! rg --quiet 'sea_music_sql_connections\{state="open"\} [1-9][0-9]*' /tmp/sea-music-metrics.txt ||
   ! rg --quiet 'sea_music_redis_connections\{state="total"\} [1-9][0-9]*' /tmp/sea-music-metrics.txt; then
    echo "SQL or Redis USE metric was not exposed" >&2
    exit 1
fi
if ! rg --quiet 'sea_music_processing_jobs\{state="succeeded"\} 1' /tmp/sea-music-metrics.txt ||
   ! rg --quiet 'sea_music_counter_drift_total [1-9][0-9]*' /tmp/sea-music-metrics.txt; then
    echo "processing backlog or drift metric was not exposed" >&2
    exit 1
fi

cleanup
trap - EXIT INT TERM

echo "verification complete"
