package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
	"t3z/api-gateway/internal/services"
)

type contextKey string

const UserContextKey contextKey = "firebase_user"

type AdminHandler struct {
	db       *database.DB
	client   *services.ClientService
	verifier *security.FirebaseVerifier
}

func NewAdminHandler(db *database.DB, cs *services.ClientService, verifier *security.FirebaseVerifier) *AdminHandler {
	return &AdminHandler{
		db:       db,
		client:   cs,
		verifier: verifier,
	}
}

func (h *AdminHandler) RequireAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Authentication required"})
			return
		}

		token := strings.TrimSpace(authHeader[7:])
		user, err := h.verifier.VerifyIDToken(token)
		if err != nil {
			respondJSON(w, http.StatusUnauthorized, models.ErrorResponse{Detail: "Invalid Firebase ID token"})
			return
		}

		if !user.Admin {
			respondJSON(w, http.StatusForbidden, models.ErrorResponse{Detail: "Admin access required"})
			return
		}

		ctx := context.WithValue(r.Context(), UserContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *AdminHandler) CreateClient(w http.ResponseWriter, r *http.Request) {
	var req models.CreateClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ClientID == "" || req.ClientName == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Invalid client payload"})
		return
	}

	secret, err := h.client.CreateClient(req.ClientID, req.ClientName)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
			respondJSON(w, http.StatusConflict, models.ErrorResponse{Detail: "Client already exists"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, models.CreateClientResponse{
		ClientID:     req.ClientID,
		ClientName:   req.ClientName,
		ClientSecret: secret,
	})
}

func (h *AdminHandler) RotateSecret(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "client_id")
	if clientID == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Missing client_id"})
		return
	}

	var clientName string
	err := h.db.QueryRow(`
		SELECT client_name
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&clientName)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondJSON(w, http.StatusNotFound, models.ErrorResponse{Detail: "Client not found"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Database error"})
		return
	}

	secret, err := h.client.RotateClientSecret(clientID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, models.CreateClientResponse{
		ClientID:     clientID,
		ClientName:   clientName,
		ClientSecret: secret,
	})
}

func (h *AdminHandler) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	clientID := chi.URLParam(r, "client_id")
	if clientID == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Missing client_id"})
		return
	}

	var req models.CreateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Invalid workflow name"})
		return
	}

	var status string
	err := h.db.QueryRow(`
		SELECT status
		FROM clients
		WHERE client_id = ?
	`, clientID).Scan(&status)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondJSON(w, http.StatusNotFound, models.ErrorResponse{Detail: "Client not found"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, models.ErrorResponse{Detail: "Database error"})
		return
	}

	if status != "active" {
		respondJSON(w, http.StatusBadRequest, models.ErrorResponse{Detail: "Client is inactive"})
		return
	}

	res, err := h.client.ProvisionWorkflow(clientID, req.Name)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, models.ErrorResponse{Detail: "Failed to provision Windmill workflow: " + err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, res)
}
