package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/models"
)

type FirebaseService struct {
	cfg        *config.Config
	httpClient *http.Client
}

func NewFirebaseService(cfg *config.Config) *FirebaseService {
	return &FirebaseService{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *FirebaseService) SignInWithPassword(email, password string) (*models.FirebaseLoginResponse, error) {
	return s.SignInWithPasswordContext(context.Background(), email, password)
}

func (s *FirebaseService) SignInWithPasswordContext(ctx context.Context, email, password string) (*models.FirebaseLoginResponse, error) {
	if s.cfg.FirebaseAPIKey == "" {
		return nil, errors.New("missing T3Z_FIREBASE_API_KEY")
	}

	endpoint := "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=" + url.QueryEscape(s.cfg.FirebaseAPIKey)
	if emulator := os.Getenv("FIREBASE_AUTH_EMULATOR_HOST"); emulator != "" {
		endpoint = "http://" + emulator + "/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=" + url.QueryEscape(s.cfg.FirebaseAPIKey)
	}
	payload := map[string]interface{}{
		"email":             email,
		"password":          password,
		"returnSecureToken": true,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to call firebase auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("Invalid Firebase credentials")
	}

	var data struct {
		IDToken   string `json:"idToken"`
		ExpiresIn string `json:"expiresIn"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to parse firebase auth response: %w", err)
	}

	exp, _ := strconv.Atoi(data.ExpiresIn)
	if exp == 0 {
		exp = 3600
	}

	return &models.FirebaseLoginResponse{
		AccessToken: data.IDToken,
		TokenType:   "bearer",
		ExpiresIn:   exp,
	}, nil
}
