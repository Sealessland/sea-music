# Deterministic load dataset

- Seed: `20260713`
- Default size: 1,000 users and 500 published videos
- Per user: 5 deterministic follows
- Per video: 8 likes, 3 favorites, 2 comments, and 3 danmaku messages
- Categories: music, games, technology, animation, and knowledge
- Media metadata: one verified source plus deterministic playback and cover rendition rows

Generate or replace the dataset:

```sh
SEA_ALLOW_DEVELOPMENT_FIXTURES=true \
SEA_LOAD_DATASET=true \
SEA_DATABASE_URL='postgres://sea_music:local-postgres-password@127.0.0.1:25432/sea_music?sslmode=disable' \
go run -buildvcs=false ./cmd/fixture
```

The loader deletes only rows carrying the `load_user_` / `Load video` namespace and recreates the distribution in one PostgreSQL transaction.
