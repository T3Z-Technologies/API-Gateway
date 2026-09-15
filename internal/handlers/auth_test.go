package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/services"
)

func TestFirebaseLogin_MissingAPIKey(t *testing.T) {
	cfg := &config.Config{
		FirebaseAPIKey: "",
	}
	firebaseService := services.NewFirebaseService(cfg)
	handler := NewAuthHandler(cfg, nil, nil, firebaseService)

	reqBody := []byte(`{"email":"admin@t3z.in","password":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/firebase-login", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	handler.FirebaseLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Expected status 503, got %d", resp.StatusCode)
	}

	var errResp models.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if errResp.Detail != "Firebase authentication is not configured: missing T3Z_FIREBASE_API_KEY environment variable on the server." {
		t.Fatalf("Unexpected detail: %s", errResp.Detail)
	}
}

func TestFirebaseLogin_EmptyFields(t *testing.T) {
	cfg := &config.Config{
		FirebaseAPIKey: "fake-key",
	}
	firebaseService := services.NewFirebaseService(cfg)
	handler := NewAuthHandler(cfg, nil, nil, firebaseService)

	reqBody := []byte(`{"email":"","password":""}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/firebase-login", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()

	handler.FirebaseLogin(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Expected status 400, got %d", resp.StatusCode)
	}
}
