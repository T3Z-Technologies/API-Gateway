package services

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
)

func TestClientService_CreateClient_WithWindmill(t *testing.T) {
	cfg := &config.Config{
		WindmillBaseURL:   "http://127.0.0.1:3001",
		WindmillWorkspace: "t3z",
		WindmillToken:     "wm_t3z_gateway_2026_supertoken",
	}

	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open sqlite: %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE clients (
			client_id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			n8n_folder_id TEXT,
			n8n_project_id TEXT,
			windmill_folder TEXT
		);
		CREATE TABLE client_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			client_id TEXT NOT NULL,
			secret_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			revoked_at TEXT,
			FOREIGN KEY (client_id) REFERENCES clients(client_id)
		);
		CREATE TABLE client_workflows (
			client_id TEXT NOT NULL,
			workflow_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			n8n_workflow_id TEXT,
			workflow_name TEXT,
			webhook_path TEXT,
			created_at TEXT NOT NULL DEFAULT '',
			windmill_path TEXT,
			PRIMARY KEY (client_id, workflow_id),
			FOREIGN KEY (client_id) REFERENCES clients(client_id)
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	db := &database.DB{DB: sqlDB}
	wm := NewWindmillService(cfg)
	clientSvc := NewClientService(cfg, db, wm)

	clientID := "cl_test_wm"
	secret, err := clientSvc.CreateClient(clientID, "Test Client")
	if err != nil {
		t.Fatalf("CreateClient failed: %v", err)
	}

	if secret == "" {
		t.Fatal("Expected non-empty secret")
	}

	// Test Workflow Provisioning in Windmill
	wf, err := clientSvc.ProvisionWorkflow(clientID, "Order Processing")
	if err != nil {
		t.Fatalf("ProvisionWorkflow failed: %v", err)
	}

	if wf.WindmillPath == "" {
		t.Fatal("Expected non-empty WindmillPath")
	}

	// Clean up Windmill script and folder
	wm.DeleteWorkflowScript(wf.WindmillPath)
	wm.DeleteFolder(clientID)
}

func TestClientService_ManagementMethods(t *testing.T) {
	cfg := &config.Config{
		PublicURL:         "https://api.t3z.in",
		WindmillBaseURL:   "http://127.0.0.1:3001",
		WindmillWorkspace: "t3z",
		WindmillToken:     "test-token",
	}

	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open sqlite: %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE clients (
			client_id TEXT PRIMARY KEY,
			client_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			n8n_folder_id TEXT,
			n8n_project_id TEXT,
			windmill_folder TEXT
		);
		CREATE TABLE client_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			client_id TEXT NOT NULL,
			secret_hash TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			revoked_at TEXT,
			FOREIGN KEY (client_id) REFERENCES clients(client_id)
		);
		CREATE TABLE client_workflows (
			client_id TEXT NOT NULL,
			workflow_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			n8n_workflow_id TEXT,
			workflow_name TEXT,
			webhook_path TEXT,
			created_at TEXT NOT NULL DEFAULT '',
			windmill_path TEXT,
			PRIMARY KEY (client_id, workflow_id),
			FOREIGN KEY (client_id) REFERENCES clients(client_id)
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	db := &database.DB{DB: sqlDB}
	wm := NewWindmillService(cfg)
	clientSvc := NewClientService(cfg, db, wm)

	// Seed client 1
	_, err = sqlDB.Exec(`
		INSERT INTO clients (client_id, client_name, status, windmill_folder)
		VALUES ('client_alpha', 'Alpha Corp', 'active', 'client_alpha')
	`)
	if err != nil {
		t.Fatalf("Insert client failed: %v", err)
	}

	_, err = sqlDB.Exec(`
		INSERT INTO client_credentials (client_id, secret_hash, status, created_at)
		VALUES ('client_alpha', 'hash123', 'active', '2026-09-14T10:00:00Z')
	`)
	if err != nil {
		t.Fatalf("Insert cred failed: %v", err)
	}

	_, err = sqlDB.Exec(`
		INSERT INTO client_workflows (client_id, workflow_id, windmill_path, workflow_name, webhook_path, status, created_at)
		VALUES ('client_alpha', 'wf_001', 'f/client_alpha/wf_001', 'Lead Flow', 'client_alpha/wf_001', 'active', '2026-09-14T10:05:00Z'),
		       ('client_alpha', 'wf_002', 'f/client_alpha/wf_002', 'Support Flow', 'client_alpha/wf_002', 'active', '2026-09-14T10:10:00Z')
	`)
	if err != nil {
		t.Fatalf("Insert workflows failed: %v", err)
	}

	// 1. Test ListClients
	clients, err := clientSvc.ListClients()
	if err != nil {
		t.Fatalf("ListClients failed: %v", err)
	}
	if len(clients) != 1 {
		t.Fatalf("Expected 1 client, got %d", len(clients))
	}
	if clients[0].ClientID != "client_alpha" || clients[0].WorkflowsCount != 2 {
		t.Fatalf("Unexpected client data: %+v", clients[0])
	}

	// 2. Test GetClient
	detail, err := clientSvc.GetClient("client_alpha")
	if err != nil {
		t.Fatalf("GetClient failed: %v", err)
	}
	if detail.ClientID != "client_alpha" || len(detail.Credentials) != 1 || len(detail.Workflows) != 2 {
		t.Fatalf("Unexpected detail: %+v", detail)
	}

	// 3. Test ListClientWorkflows
	clientWfs, err := clientSvc.ListClientWorkflows("client_alpha")
	if err != nil {
		t.Fatalf("ListClientWorkflows failed: %v", err)
	}
	if len(clientWfs) != 2 {
		t.Fatalf("Expected 2 workflows, got %d", len(clientWfs))
	}
	if clientWfs[0].WebhookURL != "https://api.t3z.in/apis/v1/webhooks/wf_001" {
		t.Fatalf("Unexpected webhook URL: %s", clientWfs[0].WebhookURL)
	}

	// 4. Test ListAllWorkflows
	allWfs, err := clientSvc.ListAllWorkflows()
	if err != nil {
		t.Fatalf("ListAllWorkflows failed: %v", err)
	}
	if len(allWfs) != 2 {
		t.Fatalf("Expected 2 workflows across all clients, got %d", len(allWfs))
	}

	// 5. Test DeleteWorkflow
	err = clientSvc.DeleteWorkflow("client_alpha", "wf_001")
	if err != nil {
		t.Fatalf("DeleteWorkflow failed: %v", err)
	}
	remainingWfs, _ := clientSvc.ListClientWorkflows("client_alpha")
	if len(remainingWfs) != 1 {
		t.Fatalf("Expected 1 workflow after delete, got %d", len(remainingWfs))
	}

	// 6. Test DeleteClient
	err = clientSvc.DeleteClient("client_alpha")
	if err != nil {
		t.Fatalf("DeleteClient failed: %v", err)
	}
	afterClients, _ := clientSvc.ListClients()
	if len(afterClients) != 0 {
		t.Fatalf("Expected 0 clients after delete, got %d", len(afterClients))
	}
}

