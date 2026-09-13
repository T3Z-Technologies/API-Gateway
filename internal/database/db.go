package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
	"t3z/api-gateway/internal/config"
)

type DB struct {
	*sql.DB
}

func InitDB(cfg *config.Config) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	dsn := fmt.Sprintf("%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)", cfg.DBPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping sqlite database: %w", err)
	}

	d := &DB{db}
	if err := d.migrate(); err != nil {
		return nil, fmt.Errorf("database migration failed: %w", err)
	}

	return d, nil
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS clients (
		client_id TEXT PRIMARY KEY,
		client_name TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		windmill_folder TEXT
	);

	CREATE TABLE IF NOT EXISTS client_credentials (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_id TEXT NOT NULL,
		secret_hash TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		revoked_at TEXT,
		FOREIGN KEY (client_id)
			REFERENCES clients(client_id)
			ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS client_workflows (
		client_id TEXT NOT NULL,
		workflow_id TEXT NOT NULL,
		windmill_path TEXT,
		workflow_name TEXT,
		webhook_path TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		PRIMARY KEY (client_id, workflow_id),
		FOREIGN KEY (client_id)
			REFERENCES clients(client_id)
			ON DELETE CASCADE
	);
	`
	if _, err := d.Exec(schema); err != nil {
		return err
	}

	// Dynamic column migrations for existing SQLite database
	ensureColumns := []struct {
		table      string
		column     string
		definition string
	}{
		{"clients", "windmill_folder", "TEXT"},
		{"clients", "n8n_folder_id", "TEXT"},
		{"clients", "n8n_project_id", "TEXT"},
		{"client_workflows", "windmill_path", "TEXT"},
		{"client_workflows", "n8n_workflow_id", "TEXT"},
		{"client_workflows", "workflow_name", "TEXT"},
		{"client_workflows", "webhook_path", "TEXT"},
		{"client_workflows", "created_at", "TEXT NOT NULL DEFAULT ''"},
	}

	for _, c := range ensureColumns {
		if err := d.ensureColumn(c.table, c.column, c.definition); err != nil {
			return err
		}
	}

	return nil
}

func (d *DB) ensureColumn(table, column, definition string) error {
	rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			exists = true
			break
		}
	}

	if !exists {
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)
		if _, err := d.Exec(query); err != nil {
			return fmt.Errorf("failed to add column %s to %s: %w", column, table, err)
		}
	}

	return nil
}
