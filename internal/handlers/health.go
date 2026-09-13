package handlers

import (
	"encoding/json"
	"net/http"

	"t3z/api-gateway/internal/models"
)

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(models.HealthResponse{Status: "ok"})
}
