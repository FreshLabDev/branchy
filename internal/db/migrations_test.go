// SPDX-License-Identifier: Apache-2.0
package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListMigrationsOrdersSQLFiles(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"010_later.sql",
		"001_init.sql",
		"notes.txt",
		"002_next.sql",
	}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- sql"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	migrations, err := ListMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 3 {
		t.Fatalf("migrations = %d, want 3", len(migrations))
	}
	for i, want := range []int{1, 2, 10} {
		if migrations[i].Version != want {
			t.Fatalf("migration %d version = %d, want %d", i, migrations[i].Version, want)
		}
	}
}
