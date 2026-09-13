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
		WebhookURL:   fmt.Sprintf("%s/apis/webhooks/%s", baseURL, workflowID),
		Status:       "active",
	}, nil
}
