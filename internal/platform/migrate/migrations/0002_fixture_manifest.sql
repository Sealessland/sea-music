CREATE TABLE public.fixture_manifest (
    fixture_name text PRIMARY KEY,
    seed bigint NOT NULL,
    fixture_version integer NOT NULL,
    loaded_at timestamptz NOT NULL
);
