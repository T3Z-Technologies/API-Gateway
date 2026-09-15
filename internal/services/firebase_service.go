package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/models"
)

var (
	ErrMissingFirebaseAPIKey = errors.New("missing T3Z_FIREBASE_API_KEY")
)

type googleAuthErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Errors  []struct {
			Message string `json:"message"`
			Domain  string `json:"domain"`
			Reason  string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

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
		return nil, ErrMissingFirebaseAPIKey
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
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[Firebase Auth] Error response (HTTP %d): %s", resp.StatusCode, string(respBody))

		var errResp googleAuthErrorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil && errResp.Error.Message != "" {
			switch strings.ToUpper(errResp.Error.Message) {
			case "EMAIL_NOT_FOUND", "INVALID_PASSWORD", "INVALID_LOGIN_CREDENTIALS":
				return nil, errors.New("invalid email or password")
			case "USER_DISABLED":
				return nil, errors.New("user account has been disabled")
			case "API_KEY_INVALID":
				return nil, errors.New("invalid Firebase Web API key configured on server")
			case "OPERATION_NOT_ALLOWED":
				return nil, errors.New("Email/Password sign-in provider is disabled in Firebase Console")
			case "TOO_MANY_ATTEMPTS_TRY_LATER":
				return nil, errors.New("access temporarily disabled due to many failed attempts; try again later")
			case "CONFIGURATION_NOT_FOUND":
				return nil, errors.New("Firebase Identity Toolkit is not configured in Google Cloud project")
			default:
				return nil, fmt.Errorf("Firebase authentication error: %s", errResp.Error.Message)
			}
		}
		return nil, fmt.Errorf("Firebase authentication failed with HTTP %d", resp.StatusCode)
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
