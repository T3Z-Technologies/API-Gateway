package database

import (
	"os"
	"path/filepath"
	"testing"

	"t3z/api-gateway/internal/config"
)

func TestDBInitAndMigration(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "t3z-db-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dbPath := filepath.Join(tempDir, "test.db")
	cfg := &config.Config{DBPath: dbPath}

	db, err := InitDB(cfg)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Verify tables exist
	tables := []string{"clients", "client_credentials", "client_workflows"}
	for _, table := range tables {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
		if err != nil || count != 1 {
			t.Fatalf("Table %s not found in sqlite_master", table)
		}
	}

	// Verify columns exist
	var colCount int
	err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('client_workflows') WHERE name='windmill_path'").Scan(&colCount)
	if err != nil || colCount != 1 {
		t.Fatalf("Column windmill_path not found in client_workflows")
	}
}
