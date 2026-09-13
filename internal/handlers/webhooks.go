package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
	"t3z/api-gateway/internal/services"
)

var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"authorization":       true,
	"host":                true,
	"content-length":      true,
}

type WebhooksHandler struct {
	db       *database.DB
	jwt      *security.JWTService
	windmill *services.WindmillService
}

func NewWebhooksHandler(db *database.DB, jwt *security.JWTService, wm *services.WindmillService) *WebhooksHandler {
	return &WebhooksHandler{
		db:       db,
		jwt:      jwt,
		windmill: wm,
	}
}

func (h *WebhooksHandler) Gateway(w http.ResponseWriter, r *http.Request) {
	workflowID := chi.URLParam(r, "workflow_id")
	if workflowID == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Missing workflow_id"})
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Unauthorized"})
		return
	}

	token := strings.TrimSpace(authHeader[7:])
	claims, err := h.jwt.VerifyWorkflowToken(token, workflowID)
	if err != nil {
		status := http.StatusUnauthorized
		if strings.Contains(err.Error(), "not authorized") {
			status = http.StatusForbidden
		}
		respondJSON(w, status, models.ErrorResponse{Detail: err.Error()})
		return
	}

	clientID := claims.Subject

	// Verify active workflow in database and retrieve Windmill path
	var windmillPath, webhookPath sql.NullString
	err = h.db.QueryRow(`
		SELECT windmill_path, webhook_path
		FROM client_workflows
		WHERE client_id = ? AND workflow_id = ? AND status = 'active'
	`, clientID, workflowID).Scan(&windmillPath, &webhookPath)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondJSON(w, http.StatusForbidden, models.ErrorResponse{Detail: "Workflow is not authorized"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Database error"})
		return
	}

	targetPath := windmillPath.String
	if targetPath == "" {
		targetPath = webhookPath.String
	}
	if targetPath == "" {
		targetPath = fmtPath(clientID, workflowID)
	}

	// Create short-lived integration token for Google Sheets proxy
	integrationToken, err := h.jwt.CreateIntegrationToken(clientID, workflowID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Failed to create integration token"})
		return
	}

	// Filter headers
	headers := make(map[string]string)
	for k, v := range r.Header {
		lower := strings.ToLower(k)
		if !hopByHopHeaders[lower] && len(v) > 0 {
			headers[k] = v[0]
		}
	}

	// Set trusted T3Z headers
	headers["X-T3Z-Client-Id"] = clientID
	headers["X-T3Z-Workflow-Id"] = workflowID
	headers["X-T3Z-Integration-Token"] = integrationToken

	// Read and adapt body for Windmill parameters
	var bodyReader io.Reader = r.Body
	bodyBytes, err := io.ReadAll(r.Body)
	if err == nil && len(bodyBytes) > 0 {
		var rawObj map[string]interface{}
		if jsonErr := json.Unmarshal(bodyBytes, &rawObj); jsonErr == nil {
			if _, hasArgs := rawObj["args"]; !hasArgs {
				if _, hasPayload := rawObj["payload"]; !hasPayload {
					wrapped := map[string]interface{}{
						"args":    rawObj,
						"payload": rawObj,
					}
					for k, v := range rawObj {
						wrapped[k] = v
					}
					if wb, mErr := json.Marshal(wrapped); mErr == nil {
						bodyBytes = wb
					}
				}
			}
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	// Forward to Windmill upstream
	resp, err := h.windmill.ProxyWebhook(targetPath, r.Method, r.URL.Query(), bodyReader, headers)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, models.ErrorResponse{Detail: "Windmill upstream unavailable"})
		return
	}
	defer resp.Body.Close()

	// Copy response headers (excluding hop-by-hop)
	for k, v := range resp.Header {
		if !hopByHopHeaders[strings.ToLower(k)] && len(v) > 0 {
			w.Header().Set(k, v[0])
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func fmtPath(clientID, workflowID string) string {
	return "f/" + strings.ToLower(clientID) + "/" + workflowID
}
