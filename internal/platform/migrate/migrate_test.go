package migrate_test

import (
	"testing"
	"testing/fstest"

	"github.com/sealessland/sea-music/internal/platform/migrate"
)

// TestLoadOrdersMigrationsAndCalculatesStableChecksums verifies that Load sorts migrations by version and produces nonempty, deterministic checksums across repeated loads.
func TestLoadOrdersMigrationsAndCalculatesStableChecksums(t *testing.T) {
	files := fstest.MapFS{
		"0002_second.sql": {Data: []byte("select 2;\n")},
		"0001_first.sql":  {Data: []byte("select 1;\n")},
	}

	first, err := migrate.Load(files)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	second, err := migrate.Load(files)
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}

	if len(first) != 2 {
		t.Fatalf("len(migrations) = %d, want 2", len(first))
	}
	if first[0].Version != "0001" || first[1].Version != "0002" {
		t.Fatalf("versions = [%s, %s], want [0001, 0002]", first[0].Version, first[1].Version)
	}
	if first[0].Checksum == "" || first[0].Checksum != second[0].Checksum {
		t.Fatalf("checksum is empty or unstable: %q != %q", first[0].Checksum, second[0].Checksum)
	}
}

// TestLoadRejectsInvalidFileName verifies that Load returns an error for a migration filename without the required version prefix.
func TestLoadRejectsInvalidFileName(t *testing.T) {
	_, err := migrate.Load(fstest.MapFS{
		"initial.sql": {Data: []byte("select 1;")},
	})
	if err == nil {
		t.Fatal("Load() error = nil, want invalid-name error")
	}
}

// TestLoadRejectsDuplicateVersion verifies that Load returns an error when multiple migration files declare the same version.
func TestLoadRejectsDuplicateVersion(t *testing.T) {
	_, err := migrate.Load(fstest.MapFS{
		"0001_first.sql":  {Data: []byte("select 1;")},
		"0001_second.sql": {Data: []byte("select 2;")},
	})
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate-version error")
	}
}
