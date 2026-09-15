package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"t3z/api-gateway/internal/config"
)

func TestFirebaseService_Errors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{
			"error": {
				"code": 400,
				"message": "INVALID_PASSWORD",
				"errors": [{"message": "INVALID_PASSWORD", "domain": "global", "reason": "invalid"}]
			}
		}`))
	}))
	defer server.Close()

	// Use the mock server address as FIREBASE_AUTH_EMULATOR_HOST
	host := server.Listener.Addr().String()
	os.Setenv("FIREBASE_AUTH_EMULATOR_HOST", host)
	defer os.Unsetenv("FIREBASE_AUTH_EMULATOR_HOST")

	cfg := &config.Config{
		FirebaseAPIKey: "test-api-key",
	}
	svc := NewFirebaseService(cfg)

	_, err := svc.SignInWithPasswordContext(context.Background(), "user@example.com", "wrongpass")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if err.Error() != "invalid email or password" {
		t.Fatalf("Expected 'invalid email or password', got '%v'", err)
	}
}

func TestFirebaseService_OperationNotAllowed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{
			"error": {
				"code": 400,
				"message": "OPERATION_NOT_ALLOWED"
			}
		}`))
	}))
	defer server.Close()

	host := server.Listener.Addr().String()
	os.Setenv("FIREBASE_AUTH_EMULATOR_HOST", host)
	defer os.Unsetenv("FIREBASE_AUTH_EMULATOR_HOST")

	cfg := &config.Config{
		FirebaseAPIKey: "test-api-key",
	}
	svc := NewFirebaseService(cfg)

	_, err := svc.SignInWithPasswordContext(context.Background(), "user@example.com", "pass")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if err.Error() != "Email/Password sign-in provider is disabled in Firebase Console" {
		t.Fatalf("Unexpected error: %v", err)
	}
}
