package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"t3z/api-gateway/internal/models"
)

func TestHealthHandler(t *testing.T) {
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	HealthHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	var data models.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data.Status != "ok" {
		t.Fatalf("Expected status 'ok', got '%s'", data.Status)
	}
}
