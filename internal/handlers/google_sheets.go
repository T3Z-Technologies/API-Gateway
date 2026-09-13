package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
	"t3z/api-gateway/internal/services"
)

type GoogleSheetsHandler struct {
	cfg    *config.Config
	db     *database.DB
	jwt    *security.JWTService
	sheets *services.GoogleSheetsService
}

func NewGoogleSheetsHandler(cfg *config.Config, db *database.DB, jwt *security.JWTService, sheets *services.GoogleSheetsService) *GoogleSheetsHandler {
	return &GoogleSheetsHandler{
		cfg:    cfg,
		db:     db,
		jwt:    jwt,
		sheets: sheets,
	}
}

func (h *GoogleSheetsHandler) ensureActiveClient(clientID string) error {
	var status string
	err := h.db.QueryRow("SELECT status FROM clients WHERE client_id = ?", clientID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("Client not found")
		}
		return err
	}
	if status != "active" {
		return errors.New("Client is inactive")
	}
	return nil
}

func (h *GoogleSheetsHandler) ensureActiveWorkflow(clientID, workflowID string) error {
	var id string
	err := h.db.QueryRow("SELECT workflow_id FROM client_workflows WHERE client_id = ? AND workflow_id = ? AND status = 'active'", clientID, workflowID).Scan(&id)
	if err != nil {
		return errors.New("Workflow is not authorized")
	}
	return nil
}

func (h *GoogleSheetsHandler) Authorize(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "client_id")
	if err := h.ensureActiveClient(clientID); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "Client not found" {
			status = http.StatusNotFound
		}
		respondJSON(w, status, models.ErrorResponse{Detail: err.Error()})
		return
	}

	user, _ := r.Context().Value(UserContextKey).(*security.FirebaseUser)
	uid := ""
	if user != nil {
		uid = user.UID
	}

	authURL, err := h.sheets.CreateAuthorizationURL(clientID, uid)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, models.GoogleAuthorizationResponse{
		AuthorizationURL: authURL,
		ExpiresIn:        services.OAuthStateTTLSeconds,
	})
}

func (h *GoogleSheetsHandler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || len(state) < 20 {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Missing code or invalid state"})
		return
	}

	result, err := h.sheets.CompleteAuthorization(code, state)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: err.Error()})
		return
	}

	if h.cfg.GoogleOAuthSuccessRedirect != "" {
		clientID, _ := result["client_id"].(string)
		q := url.Values{
			"google_sheets": {"connected"},
			"client_id":     {clientID},
		}
		sep := "?"
		if strings.Contains(h.cfg.GoogleOAuthSuccessRedirect, "?") {
			sep = "&"
		}
		http.Redirect(w, r, h.cfg.GoogleOAuthSuccessRedirect+sep+q.Encode(), http.StatusFound)
		return
	}

	respondJSON(w, http.StatusOK, result)
}

func (h *GoogleSheetsHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "client_id")
	if err := h.ensureActiveClient(clientID); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "Client not found" {
			status = http.StatusNotFound
		}
		respondJSON(w, status, models.ErrorResponse{Detail: err.Error()})
		return
	}

	res, err := h.sheets.GetConnection(clientID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, res)
}

func (h *GoogleSheetsHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "client_id")
	if err := h.ensureActiveClient(clientID); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "Client not found" {
			status = http.StatusNotFound
		}
		respondJSON(w, status, models.ErrorResponse{Detail: err.Error()})
		return
	}

	if err := h.sheets.RevokeConnection(clientID); err != nil {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *GoogleSheetsHandler) BatchGetValues(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Unauthorized"})
		return
	}

	claims, err := h.jwt.VerifyIntegrationToken(strings.TrimSpace(authHeader[7:]))
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: err.Error()})
		return
	}

	clientID := claims.Subject
	workflowID := claims.Workflow

	if err := h.ensureActiveWorkflow(clientID, workflowID); err != nil {
		respondJSON(w, http.StatusForbidden, models.ErrorResponse{Detail: err.Error()})
		return
	}

	var req models.GoogleSheetsBatchGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SpreadsheetID == "" || len(req.Ranges) == 0 {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Invalid spreadsheet batchGet payload"})
		return
	}

	if req.MajorDimension == "" {
		req.MajorDimension = "ROWS"
	}

	res, err := h.sheets.BatchGetValues(clientID, req.SpreadsheetID, req.Ranges, req.MajorDimension)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, res)
}
