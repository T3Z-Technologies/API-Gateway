package security

import (
	"crypto/rsa"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"t3z/api-gateway/internal/config"
)

type JWTService struct {
	cfg        *config.Config
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
}

func NewJWTService(cfg *config.Config) (*JWTService, error) {
	privBytes, err := os.ReadFile(cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key from %s: %w", cfg.PrivateKeyPath, err)
	}

	privKey, err := jwt.ParseRSAPrivateKeyFromPEM(privBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
	}

	pubBytes, err := os.ReadFile(cfg.PublicKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read public key from %s: %w", cfg.PublicKeyPath, err)
	}

	pubKey, err := jwt.ParseRSAPublicKeyFromPEM(pubBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RSA public key: %w", err)
	}

	return &JWTService{
		cfg:        cfg,
		privateKey: privKey,
		publicKey:  pubKey,
	}, nil
}

type WorkflowClaims struct {
	Workflow string `json:"workflow"`
	jwt.RegisteredClaims
}

type IntegrationClaims struct {
	Workflow string `json:"workflow"`
	Scope    string `json:"scope"`
	jwt.RegisteredClaims
}

func (s *JWTService) CreateWorkflowToken(clientID, workflowID string) (string, error) {
	now := time.Now().UTC()
	claims := WorkflowClaims{
		Workflow: workflowID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.JWTIssuer,
			Subject:   clientID,
			Audience:  jwt.ClaimStrings{s.cfg.JWTAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(s.cfg.JWTTTLSeconds) * time.Second)),
			ID:        fmt.Sprintf("%d", now.UnixNano()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}

func (s *JWTService) VerifyWorkflowToken(tokenString, requestedWorkflowID string) (*WorkflowClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &WorkflowClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	}, jwt.WithIssuer(s.cfg.JWTIssuer), jwt.WithAudience(s.cfg.JWTAudience))

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errors.New("Token expired")
		}
		return nil, errors.New("Invalid token")
	}

	claims, ok := token.Claims.(*WorkflowClaims)
	if !ok || !token.Valid {
		return nil, errors.New("Invalid token")
	}

	if claims.Subject == "" || claims.Workflow == "" {
		return nil, errors.New("Invalid token")
	}

	if subtle.ConstantTimeCompare([]byte(claims.Workflow), []byte(requestedWorkflowID)) != 1 {
		return nil, errors.New("Token is not authorized for this workflow")
	}

	return claims, nil
}

func (s *JWTService) CreateIntegrationToken(clientID, workflowID string) (string, error) {
	now := time.Now().UTC()
	claims := IntegrationClaims{
		Workflow: workflowID,
		Scope:    "google_sheets:read",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.cfg.JWTIssuer,
			Subject:   clientID,
			Audience:  jwt.ClaimStrings{s.cfg.IntegrationJWTAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(s.cfg.IntegrationJWTTTLSeconds) * time.Second)),
			ID:        fmt.Sprintf("%d", now.UnixNano()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(s.privateKey)
}

func (s *JWTService) VerifyIntegrationToken(tokenString string) (*IntegrationClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &IntegrationClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.publicKey, nil
	}, jwt.WithIssuer(s.cfg.JWTIssuer), jwt.WithAudience(s.cfg.IntegrationJWTAudience))

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, errors.New("Integration token expired")
		}
		return nil, errors.New("Invalid integration token")
	}

	claims, ok := token.Claims.(*IntegrationClaims)
	if !ok || !token.Valid {
		return nil, errors.New("Invalid integration token")
	}

	if claims.Subject == "" || claims.Workflow == "" || claims.Scope != "google_sheets:read" {
		return nil, errors.New("Invalid integration scope")
	}

	return claims, nil
}
