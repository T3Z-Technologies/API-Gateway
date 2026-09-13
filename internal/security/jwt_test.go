package security

import (
	"path/filepath"
	"testing"

	"t3z/api-gateway/internal/config"
)

func TestJWTCreationAndVerification(t *testing.T) {
	cfg := &config.Config{
		PrivateKeyPath:           filepath.Join("..", "..", "keys", "private.pem"),
		PublicKeyPath:            filepath.Join("..", "..", "keys", "public.pem"),
		JWTIssuer:                "t3z.in",
		JWTAudience:              "t3z-api",
		JWTTTLSeconds:            3600,
		IntegrationJWTAudience:   "t3z-integrations",
		IntegrationJWTTTLSeconds: 300,
	}

	jwtSvc, err := NewJWTService(cfg)
	if err != nil {
		t.Fatalf("Failed to initialize JWT service: %v", err)
	}

	clientID := "client_test"
	workflowID := "wf_12345"

	// 1. Workflow Token
	token, err := jwtSvc.CreateWorkflowToken(clientID, workflowID)
	if err != nil {
		t.Fatalf("CreateWorkflowToken failed: %v", err)
	}

	claims, err := jwtSvc.VerifyWorkflowToken(token, workflowID)
	if err != nil {
		t.Fatalf("VerifyWorkflowToken failed: %v", err)
	}

	if claims.Subject != clientID || claims.Workflow != workflowID {
		t.Fatalf("Claims mismatch: got subject=%s, workflow=%s", claims.Subject, claims.Workflow)
	}

	// Negative test: wrong workflow
	_, err = jwtSvc.VerifyWorkflowToken(token, "wf_other")
	if err == nil {
		t.Fatalf("VerifyWorkflowToken should have failed for wrong workflow")
	}

	// 2. Integration Token
	intToken, err := jwtSvc.CreateIntegrationToken(clientID, workflowID)
	if err != nil {
		t.Fatalf("CreateIntegrationToken failed: %v", err)
	}

	intClaims, err := jwtSvc.VerifyIntegrationToken(intToken)
	if err != nil {
		t.Fatalf("VerifyIntegrationToken failed: %v", err)
	}

	if intClaims.Subject != clientID || intClaims.Scope != "google_sheets:read" {
		t.Fatalf("Integration claims mismatch: got subject=%s, scope=%s", intClaims.Subject, intClaims.Scope)
	}
}
