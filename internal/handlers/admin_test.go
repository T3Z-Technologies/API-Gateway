package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/services"
)

func setupTestAdmin(t *testing.T) (*chi.Mux, *services.ClientService, *httptest.Server) {
	mockWM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))

	cfg := &config.Config{
		PublicURL:         "https://api.t3z.in",
		WindmillBaseURL:   mockWM.URL,
		WindmillWorkspace: "t3z",
		WindmillToken:     "test-token",
	}

	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open sqlite: %v", err)
	}

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
	wm := services.NewWindmillService(cfg)
	clientSvc := services.NewClientService(cfg, db, wm)
	adminHandler := NewAdminHandler(db, clientSvc, nil)

	r := chi.NewRouter()
	r.Route("/admin", func(adm chi.Router) {
		adm.Get("/clients", adminHandler.ListClients)
		adm.Post("/clients", adminHandler.CreateClient)
		adm.Get("/clients/{client_id}", adminHandler.GetClient)
		adm.Delete("/clients/{client_id}", adminHandler.DeleteClient)
		adm.Post("/clients/{client_id}/rotate-secret", adminHandler.RotateSecret)
		adm.Get("/clients/{client_id}/workflows", adminHandler.ListClientWorkflows)
		adm.Post("/clients/{client_id}/workflows", adminHandler.CreateWorkflow)
		adm.Delete("/clients/{client_id}/workflows/{workflow_id}", adminHandler.DeleteWorkflow)
		adm.Get("/workflows", adminHandler.ListAllWorkflows)
	})

	return r, clientSvc, mockWM
}

func TestAdminHandler_ClientManagement(t *testing.T) {
	r, clientSvc, mockWM := setupTestAdmin(t)
	defer mockWM.Close()

	// Create client via service
	_, err := clientSvc.CreateClient("test_client", "Test Client")
	if err != nil {
		t.Fatalf("CreateClient failed: %v", err)
	}

	// 1. GET /admin/clients
	req := httptest.NewRequest("GET", "/admin/clients", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var clients []models.ClientListItem
	if err := json.NewDecoder(w.Body).Decode(&clients); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if len(clients) != 1 || clients[0].ClientID != "test_client" {
		t.Fatalf("Unexpected clients: %+v", clients)
	}

	// 2. GET /admin/clients/{client_id}
	req = httptest.NewRequest("GET", "/admin/clients/test_client", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	var detail models.ClientDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if detail.ClientID != "test_client" || len(detail.Credentials) != 1 {
		t.Fatalf("Unexpected detail: %+v", detail)
	}

	// 3. GET /admin/workflows (initially 0)
	req = httptest.NewRequest("GET", "/admin/workflows", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	var workflows []models.WorkflowListItem
	if err := json.NewDecoder(w.Body).Decode(&workflows); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if len(workflows) != 0 {
		t.Fatalf("Expected 0 workflows, got %d", len(workflows))
	}

	// 4. DELETE /admin/clients/{client_id}
	req = httptest.NewRequest("DELETE", "/admin/clients/test_client", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 5. GET /admin/clients/{client_id} should now return 404
	req = httptest.NewRequest("GET", "/admin/clients/test_client", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected 404, got %d", w.Code)
	}
}
