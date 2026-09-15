package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
	"t3z/api-gateway/internal/services"
)

type AuthHandler struct {
	cfg      *config.Config
	db       *database.DB
	jwt      *security.JWTService
	firebase *services.FirebaseService
}

func NewAuthHandler(cfg *config.Config, db *database.DB, jwt *security.JWTService, fb *services.FirebaseService) *AuthHandler {
	return &AuthHandler{
		cfg:      cfg,
		db:       db,
		jwt:      jwt,
		firebase: fb,
	}
}

func (h *AuthHandler) FirebaseLogin(w http.ResponseWriter, r *http.Request) {
	var req models.FirebaseLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Invalid request payload"})
		return
	}

	if req.Email == "" || req.Password == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Email and password are required"})
		return
	}

	resp, err := h.firebase.SignInWithPassword(req.Email, req.Password)
	if err != nil {
		if errors.Is(err, services.ErrMissingFirebaseAPIKey) {
			respondJSON(w, http.StatusServiceUnavailable, models.ErrorResponse{
				Detail: "Firebase authentication is not configured: missing T3Z_FIREBASE_API_KEY environment variable on the server.",
			})
			return
		}
		errMsg := err.Error()
		if strings.Contains(errMsg, "invalid Firebase Web API key") || strings.Contains(errMsg, "disabled in Firebase Console") || strings.Contains(errMsg, "not configured") {
			respondJSON(w, http.StatusBadGateway, models.ErrorResponse{Detail: errMsg})
			return
		}
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: errMsg})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *AuthHandler) IssueToken(w http.ResponseWriter, r *http.Request) {
	var req models.TokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.WorkflowID == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Invalid request body"})
		return
	}

	clientID, clientSecret, err := security.GetBasicCredentials(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Basic")
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Unauthorized"})
		return
	}

	// 1. Verify Client is active
	var clientStatus string
	err = h.db.QueryRow(`
		SELECT status
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&clientStatus)

	if err != nil || clientStatus != "active" {
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Unauthorized"})
		return
	}

	// 2. Verify Credentials
	rows, err := h.db.Query(`
		SELECT secret_hash
		FROM client_credentials
		WHERE client_id = ? AND status = 'active'
		ORDER BY id DESC
	`, clientID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Internal database error"})
		return
	}
	defer rows.Close()

	authenticated := false
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err == nil {
			if security.VerifySecret(hash, clientSecret) {
				authenticated = true
				break
			}
		}
	}

	if !authenticated {
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Unauthorized"})
		return
	}

	// 3. Verify workflow is active for this client
	var wfID string
	err = h.db.QueryRow(`
		SELECT workflow_id
		FROM client_workflows
		WHERE client_id = ? AND workflow_id = ? AND status = 'active'
	`, clientID, req.WorkflowID).Scan(&wfID)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondJSON(w, http.StatusForbidden, models.ErrorResponse{Detail: "Forbidden"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Internal database error"})
		return
	}

	// 4. Issue RS256 token
	token, err := h.jwt.CreateWorkflowToken(clientID, req.WorkflowID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Failed to create workflow token"})
		return
	}

	respondJSON(w, http.StatusOK, models.TokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   h.cfg.JWTTTLSeconds,
		WorkflowID:  req.WorkflowID,
	})
}

func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
