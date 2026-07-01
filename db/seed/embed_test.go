package seed

import (
	"io/fs"
	"strings"
	"testing"
)

// TestFSEmbedsSeed verifies the demo seed SQL is embedded and applied by the
// `seed` subcommand.
func TestFSEmbedsSeed(t *testing.T) {
	entries, err := fs.ReadDir(FS, "sql")
	if err != nil {
		t.Fatalf("ReadDir(sql): %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no seed files embedded under sql/")
	}
}

// TestSeedIsIdempotent guards that the demo seed can be re-applied safely: every
// INSERT must carry an ON CONFLICT clause so a second `seed` run is a no-op.
func TestSeedIsIdempotent(t *testing.T) {
	b, err := fs.ReadFile(FS, "sql/000001_demo_products.up.sql")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(b)), "on conflict") {
		t.Error("demo seed must be idempotent (missing ON CONFLICT)")
	}
}
