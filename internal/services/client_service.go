package services

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
)

type ClientService struct {
	cfg      *config.Config
	db       *database.DB
	windmill *WindmillService
}

func NewClientService(cfg *config.Config, db *database.DB, wm *WindmillService) *ClientService {
	return &ClientService{
		cfg:      cfg,
		db:       db,
		windmill: wm,
	}
}

func GenerateWorkflowID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	clean := base64.RawURLEncoding.EncodeToString(b)
	clean = strings.ReplaceAll(clean, "-", "")
	clean = strings.ReplaceAll(clean, "_", "")
	if len(clean) > 16 {
		clean = clean[:16]
	}
	return "wf_" + clean
}

func GenerateClientSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "t3z_live_" + base64.RawURLEncoding.EncodeToString(b)
}

func (s *ClientService) CreateClient(clientID, clientName string) (string, error) {
	clientSecret := GenerateClientSecret()
	secretHash, err := security.HashSecret(clientSecret)
	if err != nil {
		return "", fmt.Errorf("failed to hash secret: %w", err)
	}

	folderName := strings.ToLower(clientID)
	// Create folder in Windmill
	if err := s.windmill.CreateFolder(folderName); err != nil {
		// Log or return error if Windmill fails
		return "", fmt.Errorf("failed to create Windmill folder: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		s.windmill.DeleteFolder(folderName)
		return "", err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.Exec(`
		INSERT INTO clients (client_id, client_name, status, windmill_folder)
		VALUES (?, ?, 'active', ?)
	`, clientID, clientName, folderName)
	if err != nil {
		s.windmill.DeleteFolder(folderName)
		return "", err
	}

	_, err = tx.Exec(`
		INSERT INTO client_credentials (client_id, secret_hash, status, created_at)
		VALUES (?, ?, 'active', ?)
	`, clientID, secretHash, now)
	if err != nil {
		s.windmill.DeleteFolder(folderName)
		return "", err
	}

	if err := tx.Commit(); err != nil {
		s.windmill.DeleteFolder(folderName)
		return "", err
	}

	return clientSecret, nil
}

func (s *ClientService) RotateClientSecret(clientID string) (string, error) {
	newSecret := GenerateClientSecret()
	secretHash, err := security.HashSecret(newSecret)
	if err != nil {
		return "", fmt.Errorf("failed to hash secret: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		UPDATE client_credentials
		SET status = 'revoked', revoked_at = ?
		WHERE client_id = ? AND status = 'active'
	`, now, clientID)
	if err != nil {
		return "", err
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return "", errors.New("no active credentials found for client")
	}

	_, err = tx.Exec(`
		INSERT INTO client_credentials (client_id, secret_hash, status, created_at)
		VALUES (?, ?, 'active', ?)
	`, clientID, secretHash, now)
	if err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}

	return newSecret, nil
}

func (s *ClientService) ProvisionWorkflow(clientID, name string) (*models.WorkflowResponse, error) {
	var clientStatus, folder sql.NullString
	err := s.db.QueryRow(`
		SELECT status, windmill_folder
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&clientStatus, &folder)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("Client not found")
		}
		return nil, err
	}

	if clientStatus.String != "active" {
		return nil, errors.New("Client is inactive")
	}

	folderName := folder.String
	if folderName == "" {
		folderName = strings.ToLower(clientID)
	}

	workflowID := GenerateWorkflowID()

	// Provision workflow script in Windmill
	windmillPath, err := s.windmill.CreateWorkflowScript(folderName, workflowID, name)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow in Windmill: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	webhookPath := fmt.Sprintf("%s/%s", clientID, workflowID)

	_, err = s.db.Exec(`
		INSERT INTO client_workflows (
			client_id, workflow_id, windmill_path, workflow_name, webhook_path, status, created_at
		)
		VALUES (?, ?, ?, ?, ?, 'active', ?)
	`, clientID, workflowID, windmillPath, name, webhookPath, now)

	if err != nil {
		s.windmill.DeleteWorkflowScript(windmillPath)
		return nil, fmt.Errorf("database insert failed: %w", err)
	}

	baseURL := "https://api.t3z.in"
	if s.cfg != nil && s.cfg.PublicURL != "" {
		baseURL = s.cfg.PublicURL
	}

	return &models.WorkflowResponse{
		ClientID:     clientID,
		WorkflowID:   workflowID,
		WindmillPath: windmillPath,
		Name:         name,
		WebhookURL:   fmt.Sprintf("%s/apis/v1/webhooks/%s", baseURL, workflowID),
		Status:       "active",
		CreatedAt:    now,
	}, nil
}

func (s *ClientService) getBaseURL() string {
	if s.cfg != nil && s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	return "https://api.t3z.in"
}

func (s *ClientService) ListClients() ([]models.ClientListItem, error) {
	rows, err := s.db.Query(`
		SELECT 
			c.client_id, 
			c.client_name, 
			c.status, 
			COALESCE(c.windmill_folder, ''),
			(SELECT COUNT(*) FROM client_workflows cw WHERE cw.client_id = c.client_id) as workflows_count,
			COALESCE((SELECT MIN(created_at) FROM client_credentials cc WHERE cc.client_id = c.client_id), '') as created_at
		FROM clients c
		ORDER BY c.client_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query clients: %w", err)
	}
	defer rows.Close()

	var clients []models.ClientListItem
	for rows.Next() {
		var item models.ClientListItem
		if err := rows.Scan(
			&item.ClientID,
			&item.ClientName,
			&item.Status,
			&item.WindmillFolder,
			&item.WorkflowsCount,
			&item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan client: %w", err)
		}
		clients = append(clients, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if clients == nil {
		clients = []models.ClientListItem{}
	}
	return clients, nil
}

func (s *ClientService) GetClient(clientID string) (*models.ClientDetailResponse, error) {
	var client models.ClientDetailResponse
	var windmillFolder sql.NullString
	err := s.db.QueryRow(`
		SELECT client_id, client_name, status, windmill_folder
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&client.ClientID, &client.ClientName, &client.Status, &windmillFolder)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("client not found")
		}
		return nil, fmt.Errorf("database query failed: %w", err)
	}
	client.WindmillFolder = windmillFolder.String

	// Fetch credentials
	credRows, err := s.db.Query(`
		SELECT id, status, created_at, revoked_at
		FROM client_credentials
		WHERE client_id = ?
		ORDER BY id DESC
	`, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to query credentials: %w", err)
	}
	defer credRows.Close()

	client.Credentials = []models.ClientCredentialSummary{}
	for credRows.Next() {
		var cred models.ClientCredentialSummary
		var revokedAt sql.NullString
		if err := credRows.Scan(&cred.ID, &cred.Status, &cred.CreatedAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("failed to scan credential: %w", err)
		}
		if revokedAt.Valid {
			cred.RevokedAt = &revokedAt.String
		}
		client.Credentials = append(client.Credentials, cred)
	}

	// Fetch workflows
	workflows, err := s.ListClientWorkflows(clientID)
	if err != nil {
		return nil, err
	}
	client.Workflows = workflows

	return &client, nil
}

func (s *ClientService) ListClientWorkflows(clientID string) ([]models.WorkflowResponse, error) {
	baseURL := s.getBaseURL()
	rows, err := s.db.Query(`
		SELECT workflow_id, COALESCE(windmill_path, ''), COALESCE(workflow_name, ''), status, COALESCE(created_at, '')
		FROM client_workflows
		WHERE client_id = ?
		ORDER BY workflow_id ASC
	`, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to query client workflows: %w", err)
	}
	defer rows.Close()

	var workflows []models.WorkflowResponse
	for rows.Next() {
		var wf models.WorkflowResponse
		wf.ClientID = clientID
		if err := rows.Scan(&wf.WorkflowID, &wf.WindmillPath, &wf.Name, &wf.Status, &wf.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan workflow: %w", err)
		}
		wf.WebhookURL = fmt.Sprintf("%s/apis/v1/webhooks/%s", baseURL, wf.WorkflowID)
		workflows = append(workflows, wf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if workflows == nil {
		workflows = []models.WorkflowResponse{}
	}
	return workflows, nil
}

func (s *ClientService) ListAllWorkflows() ([]models.WorkflowListItem, error) {
	baseURL := s.getBaseURL()
	rows, err := s.db.Query(`
		SELECT client_id, workflow_id, COALESCE(windmill_path, ''), COALESCE(workflow_name, ''), status, COALESCE(created_at, '')
		FROM client_workflows
		ORDER BY client_id ASC, workflow_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query workflows: %w", err)
	}
	defer rows.Close()

	var workflows []models.WorkflowListItem
	for rows.Next() {
		var wf models.WorkflowListItem
		if err := rows.Scan(&wf.ClientID, &wf.WorkflowID, &wf.WindmillPath, &wf.Name, &wf.Status, &wf.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan workflow: %w", err)
		}
		wf.WebhookURL = fmt.Sprintf("%s/apis/v1/webhooks/%s", baseURL, wf.WorkflowID)
		workflows = append(workflows, wf)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if workflows == nil {
		workflows = []models.WorkflowListItem{}
	}
	return workflows, nil
}

func (s *ClientService) DeleteWorkflow(clientID, workflowID string) error {
	var windmillPath sql.NullString
	err := s.db.QueryRow(`
		SELECT windmill_path
		FROM client_workflows
		WHERE client_id = ? AND workflow_id = ?
	`, clientID, workflowID).Scan(&windmillPath)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("workflow not found")
		}
		return fmt.Errorf("database query failed: %w", err)
	}

	// Delete from DB
	res, err := s.db.Exec(`
		DELETE FROM client_workflows
		WHERE client_id = ? AND workflow_id = ?
	`, clientID, workflowID)
	if err != nil {
		return fmt.Errorf("failed to delete workflow from database: %w", err)
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		return errors.New("workflow not found")
	}

	// Clean up script from Windmill if path exists
	if windmillPath.Valid && windmillPath.String != "" {
		s.windmill.DeleteWorkflowScript(windmillPath.String)
	}

	return nil
}

func (s *ClientService) DeleteClient(clientID string) error {
	var folder sql.NullString
	err := s.db.QueryRow(`
		SELECT windmill_folder
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&folder)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("client not found")
		}
		return fmt.Errorf("database query failed: %w", err)
	}

	// Clean up workflow flows/scripts from Windmill
	rows, err := s.db.Query(`
		SELECT windmill_path
		FROM client_workflows
		WHERE client_id = ?
	`, clientID)
	if err == nil {
		for rows.Next() {
			var wp string
			if err := rows.Scan(&wp); err == nil && wp != "" {
				s.windmill.DeleteWorkflowScript(wp)
			}
		}
		rows.Close()
	}

	// Delete client and associated records inside a transaction
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM client_workflows WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("failed to delete client workflows: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM client_credentials WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("failed to delete client credentials: %w", err)
	}

	res, err := tx.Exec(`DELETE FROM clients WHERE client_id = ?`, clientID)
	if err != nil {
		return fmt.Errorf("failed to delete client from database: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return errors.New("client not found")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Clean up folder in Windmill
	folderName := folder.String
	if folderName == "" {
		folderName = strings.ToLower(clientID)
	}
	s.windmill.DeleteFolder(folderName)

	return nil
}

