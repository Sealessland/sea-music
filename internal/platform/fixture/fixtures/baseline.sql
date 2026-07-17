INSERT INTO public.fixture_manifest (fixture_name, seed, fixture_version, loaded_at)
VALUES ('baseline', 20260712, 1, '2026-07-12T00:00:00Z')
ON CONFLICT (fixture_name) DO UPDATE SET
    seed = EXCLUDED.seed,
    fixture_version = EXCLUDED.fixture_version,
    loaded_at = EXCLUDED.loaded_at;
