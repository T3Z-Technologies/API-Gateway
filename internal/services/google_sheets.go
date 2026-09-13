package services

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/models"
	"t3z/api-gateway/internal/security"
)

const (
	GoogleAuthURL        = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL       = "https://oauth2.googleapis.com/token"
	GoogleUserInfoURL    = "https://openidconnect.googleapis.com/v1/userinfo"
	GoogleRevokeURL      = "https://oauth2.googleapis.com/revoke"
	SheetsScope          = "https://www.googleapis.com/auth/spreadsheets.readonly"
	OAuthStateTTLSeconds = 600
)

type GoogleSheetsService struct {
	cfg        *config.Config
	encryptor  *security.FernetEncryptor
	httpClient *http.Client

	saEmail      string
	saPrivateKey *rsa.PrivateKey
	projectID    string
	saToken      string
	saTokenExp   time.Time
	saMux        sync.Mutex
}

func NewGoogleSheetsService(cfg *config.Config) (*GoogleSheetsService, error) {
	var encryptor *security.FernetEncryptor
	if cfg.GoogleTokenEncryptionKey != "" {
		var err error
		encryptor, err = security.NewFernetEncryptor(cfg.GoogleTokenEncryptionKey)
		if err != nil {
			return nil, fmt.Errorf("failed to init Fernet encryptor: %w", err)
		}
	}

	s := &GoogleSheetsService{
		cfg:        cfg,
		encryptor:  encryptor,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}

	if cfg.FirebaseCredentials != "" {
		_ = s.loadServiceAccount(cfg.FirebaseCredentials)
	}

	return s, nil
}

func (s *GoogleSheetsService) loadServiceAccount(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return err
	}

	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return err
	}

	s.saEmail = sa.ClientEmail
	s.saPrivateKey = privKey
	s.projectID = sa.ProjectID
	return nil
}

func (s *GoogleSheetsService) getServiceAccountToken() (string, error) {
	s.saMux.Lock()
	defer s.saMux.Unlock()

	if s.saToken != "" && time.Now().Before(s.saTokenExp) {
		return s.saToken, nil
	}

	if s.saPrivateKey == nil || s.saEmail == "" {
		return "", errors.New("missing or invalid service account credentials")
	}

	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":   s.saEmail,
		"sub":   s.saEmail,
		"aud":   GoogleTokenURL,
		"scope": "https://www.googleapis.com/auth/datastore",
		"iat":   now.Unix(),
		"exp":   now.Add(1 * time.Hour).Unix(),
	}

	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	assertion, err := t.SignedString(s.saPrivateKey)
	if err != nil {
		return "", err
	}

	resp, err := s.httpClient.PostForm(GoogleTokenURL, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to get service account token: %s", string(body))
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}

	s.saToken = res.AccessToken
	s.saTokenExp = time.Now().Add(time.Duration(res.ExpiresIn-60) * time.Second)
	return s.saToken, nil
}

func (s *GoogleSheetsService) requireConfig() error {
	var missing []string
	if s.cfg.GoogleOAuthClientID == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_ID")
	}
	if s.cfg.GoogleOAuthClientSecret == "" {
		missing = append(missing, "GOOGLE_OAUTH_CLIENT_SECRET")
	}
	if s.cfg.GoogleOAuthRedirectURI == "" {
		missing = append(missing, "GOOGLE_OAUTH_REDIRECT_URI")
	}
	if s.cfg.GoogleTokenEncryptionKey == "" {
		missing = append(missing, "GOOGLE_TOKEN_ENCRYPTION_KEY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing configuration: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (s *GoogleSheetsService) CreateAuthorizationURL(clientID, firebaseUID string) (string, error) {
	if err := s.requireConfig(); err != nil {
		return "", err
	}

	stateBytes := make([]byte, 24)
	verifierBytes := make([]byte, 48)
	_, _ = rand.Read(stateBytes)
	_, _ = rand.Read(verifierBytes)

	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	expiresAt := time.Now().UTC().Add(time.Duration(OAuthStateTTLSeconds) * time.Second)

	// Store in Firestore: google_oauth_states/{state}
	err := s.firestoreSet(fmt.Sprintf("google_oauth_states/%s", state), map[string]interface{}{
		"client_id":     clientID,
		"firebase_uid":  firebaseUID,
		"code_verifier": verifier,
		"expires_at":    expiresAt.Format(time.RFC3339),
		"created_at":    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("failed to save oauth state to Firestore: %w", err)
	}

	params := url.Values{
		"client_id":              {s.cfg.GoogleOAuthClientID},
		"redirect_uri":           {s.cfg.GoogleOAuthRedirectURI},
		"response_type":          {"code"},
		"scope":                  {"openid email " + SheetsScope},
		"access_type":            {"offline"},
		"prompt":                 {"consent"},
		"include_granted_scopes": {"true"},
		"state":                  {state},
		"code_challenge":         {challenge},
		"code_challenge_method":  {"S256"},
	}

	return fmt.Sprintf("%s?%s", GoogleAuthURL, params.Encode()), nil
}

func (s *GoogleSheetsService) CompleteAuthorization(code, state string) (map[string]interface{}, error) {
	if err := s.requireConfig(); err != nil {
		return nil, err
	}

	docPath := fmt.Sprintf("google_oauth_states/%s", state)
	stateData, err := s.firestoreGet(docPath)
	if err != nil || stateData == nil {
		return nil, errors.New("Invalid or already-used OAuth state")
	}

	_ = s.firestoreDelete(docPath)

	expiresAtStr, _ := stateData["expires_at"].(string)
	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil || time.Now().UTC().After(expiresAt) {
		return nil, errors.New("OAuth state expired; start again")
	}

	verifier, _ := stateData["code_verifier"].(string)
	clientID, _ := stateData["client_id"].(string)
	firebaseUID, _ := stateData["firebase_uid"].(string)

	tokenResp, err := s.httpClient.PostForm(GoogleTokenURL, url.Values{
		"code":          {code},
		"client_id":     {s.cfg.GoogleOAuthClientID},
		"client_secret": {s.cfg.GoogleOAuthClientSecret},
		"redirect_uri":  {s.cfg.GoogleOAuthRedirectURI},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	})
	if err != nil || tokenResp.StatusCode != http.StatusOK {
		return nil, errors.New("Google rejected the authorization code")
	}
	defer tokenResp.Body.Close()

	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenData); err != nil {
		return nil, err
	}

	// Fetch userinfo
	userInfoReq, _ := http.NewRequest("GET", GoogleUserInfoURL, nil)
	userInfoReq.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	userResp, err := s.httpClient.Do(userInfoReq)
	if err != nil || userResp.StatusCode != http.StatusOK {
		return nil, errors.New("Could not read the authorized Google account")
	}
	defer userResp.Body.Close()

	var userData struct {
		Email string `json:"email"`
		Sub   string `json:"sub"`
	}
	_ = json.NewDecoder(userResp.Body).Decode(&userData)

	integrationDocPath := fmt.Sprintf("clients/%s/integrations/google_sheets", clientID)
	oldData, _ := s.firestoreGet(integrationDocPath)

	refreshToken := tokenData.RefreshToken
	var encryptedRefresh string
	if refreshToken != "" {
		encryptedRefresh, _ = s.encryptor.Encrypt(refreshToken)
	} else if oldData != nil {
		encryptedRefresh, _ = oldData["refresh_token_encrypted"].(string)
	}

	if encryptedRefresh == "" {
		return nil, errors.New("Google did not return offline access; revoke the app in your Google Account and try again")
	}

	scopes := strings.Split(tokenData.Scope, " ")
	hasSheetsScope := false
	for _, sc := range scopes {
		if sc == SheetsScope {
			hasSheetsScope = true
			break
		}
	}
	if !hasSheetsScope {
		return nil, errors.New("Google Sheets permission was not granted")
	}

	encryptedAccess, _ := s.encryptor.Encrypt(tokenData.AccessToken)
	tokenExpiry := time.Now().UTC().Add(time.Duration(tokenData.ExpiresIn) * time.Second)

	saveData := map[string]interface{}{
		"provider":                  "google",
		"status":                    "active",
		"email":                     userData.Email,
		"google_subject":            userData.Sub,
		"access_token_encrypted":    encryptedAccess,
		"refresh_token_encrypted":   encryptedRefresh,
		"token_expiry":              tokenExpiry.Format(time.RFC3339),
		"scopes":                    scopes,
		"connected_by_firebase_uid": firebaseUID,
		"updated_at":                time.Now().UTC().Format(time.RFC3339),
	}

	if err := s.firestoreSet(integrationDocPath, saveData); err != nil {
		return nil, fmt.Errorf("failed to save integration data: %w", err)
	}

	return map[string]interface{}{
		"client_id": clientID,
		"connected": true,
		"email":     userData.Email,
		"scopes":    scopes,
	}, nil
}

func (s *GoogleSheetsService) GetConnection(clientID string) (*models.GoogleConnectionResponse, error) {
	docPath := fmt.Sprintf("clients/%s/integrations/google_sheets", clientID)
	data, err := s.firestoreGet(docPath)
	if err != nil || data == nil {
		return &models.GoogleConnectionResponse{
			ClientID:  clientID,
			Connected: false,
			Email:     nil,
			Scopes:    []string{},
		}, nil
	}

	status, _ := data["status"].(string)
	email, _ := data["email"].(string)
	var scopes []string
	if rawScopes, ok := data["scopes"].([]interface{}); ok {
		for _, rs := range rawScopes {
			if str, ok := rs.(string); ok {
				scopes = append(scopes, str)
			}
		}
	}

	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}

	return &models.GoogleConnectionResponse{
		ClientID:  clientID,
		Connected: status == "active",
		Email:     emailPtr,
		Scopes:    scopes,
	}, nil
}

func (s *GoogleSheetsService) RevokeConnection(clientID string) error {
	docPath := fmt.Sprintf("clients/%s/integrations/google_sheets", clientID)
	data, err := s.firestoreGet(docPath)
	if err != nil || data == nil {
		return nil
	}

	encToken, _ := data["refresh_token_encrypted"].(string)
	if encToken == "" {
		encToken, _ = data["access_token_encrypted"].(string)
	}

	if encToken != "" && s.encryptor != nil {
		if token, err := s.encryptor.Decrypt(encToken); err == nil {
			_, _ = s.httpClient.PostForm(GoogleRevokeURL, url.Values{"token": {token}})
		}
	}

	return s.firestoreDelete(docPath)
}

func (s *GoogleSheetsService) ValidAccessToken(clientID string) (string, error) {
	docPath := fmt.Sprintf("clients/%s/integrations/google_sheets", clientID)
	data, err := s.firestoreGet(docPath)
	if err != nil || data == nil {
		return "", errors.New("Google Sheets is not connected for this client")
	}

	if data["status"] != "active" {
		return "", errors.New("Google Sheets connection is inactive")
	}

	now := time.Now().UTC()
	if expStr, ok := data["token_expiry"].(string); ok {
		if exp, err := time.Parse(time.RFC3339, expStr); err == nil && exp.After(now.Add(60*time.Second)) {
			encAccess, _ := data["access_token_encrypted"].(string)
			return s.encryptor.Decrypt(encAccess)
		}
	}

	// Refresh token
	encRefresh, _ := data["refresh_token_encrypted"].(string)
	refreshToken, err := s.encryptor.Decrypt(encRefresh)
	if err != nil {
		return "", errors.New("failed to decrypt refresh token")
	}

	resp, err := s.httpClient.PostForm(GoogleTokenURL, url.Values{
		"client_id":     {s.cfg.GoogleOAuthClientID},
		"client_secret": {s.cfg.GoogleOAuthClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	})
	if err != nil || resp.StatusCode != http.StatusOK {
		_ = s.firestoreSet(docPath, map[string]interface{}{"status": "reauthorization_required"})
		return "", errors.New("Google authorization expired; reconnect it")
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}

	encAccess, _ := s.encryptor.Encrypt(tr.AccessToken)
	newExpiry := now.Add(time.Duration(tr.ExpiresIn) * time.Second)

	data["access_token_encrypted"] = encAccess
	data["token_expiry"] = newExpiry.Format(time.RFC3339)
	data["updated_at"] = now.Format(time.RFC3339)
	_ = s.firestoreSet(docPath, data)

	return tr.AccessToken, nil
}

func (s *GoogleSheetsService) BatchGetValues(clientID, spreadsheetID string, ranges []string, majorDimension string) (map[string]interface{}, error) {
	accessToken, err := s.ValidAccessToken(clientID)
	if err != nil {
		return nil, err
	}

	targetURL := fmt.Sprintf("https://sheets.googleapis.com/v4/spreadsheets/%s/values:batchGet", url.PathEscape(spreadsheetID))
	q := url.Values{}
	for _, r := range ranges {
		q.Add("ranges", r)
	}
	q.Set("majorDimension", majorDimension)

	req, err := http.NewRequest("GET", targetURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		if errMsg, ok := result["error"].(map[string]interface{}); ok {
			if msg, ok := errMsg["message"].(string); ok {
				return nil, errors.New(msg)
			}
		}
		return nil, errors.New("Google Sheets request failed")
	}

	return result, nil
}

// -------------------------------------------------------------
// Firestore REST Helpers
// -------------------------------------------------------------

func (s *GoogleSheetsService) firestoreURL(docPath string) string {
	return fmt.Sprintf("https://firestore.googleapis.com/v1/projects/%s/databases/(default)/documents/%s", s.projectID, docPath)
}

func (s *GoogleSheetsService) firestoreGet(docPath string) (map[string]interface{}, error) {
	token, err := s.getServiceAccountToken()
	if err != nil {
		return nil, err
	}

	req, _ := http.NewRequest("GET", s.firestoreURL(docPath), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("firestore get status %d", resp.StatusCode)
	}

	var raw struct {
		Fields map[string]map[string]interface{} `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	res := make(map[string]interface{})
	for k, v := range raw.Fields {
		if str, ok := v["stringValue"].(string); ok {
			res[k] = str
		} else if b, ok := v["booleanValue"].(bool); ok {
			res[k] = b
		} else if arr, ok := v["arrayValue"].(map[string]interface{}); ok {
			if values, ok := arr["values"].([]interface{}); ok {
				var list []string
				for _, itm := range values {
					if m, ok := itm.(map[string]interface{}); ok {
						if sv, ok := m["stringValue"].(string); ok {
							list = append(list, sv)
						}
					}
				}
				res[k] = list
			}
		}
	}
	return res, nil
}

func (s *GoogleSheetsService) firestoreSet(docPath string, data map[string]interface{}) error {
	token, err := s.getServiceAccountToken()
	if err != nil {
		return err
	}

	fields := make(map[string]interface{})
	for k, v := range data {
		switch val := v.(type) {
		case string:
			fields[k] = map[string]interface{}{"stringValue": val}
		case bool:
			fields[k] = map[string]interface{}{"booleanValue": val}
		case []string:
			var arr []interface{}
			for _, item := range val {
				arr = append(arr, map[string]interface{}{"stringValue": item})
			}
			fields[k] = map[string]interface{}{
				"arrayValue": map[string]interface{}{"values": arr},
			}
		}
	}

	payload, err := json.Marshal(map[string]interface{}{"fields": fields})
	if err != nil {
		return err
	}

	// Use PATCH to write document
	req, _ := http.NewRequest("PATCH", s.firestoreURL(docPath), strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firestore set error %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (s *GoogleSheetsService) firestoreDelete(docPath string) error {
	token, err := s.getServiceAccountToken()
	if err != nil {
		return err
	}

	req, _ := http.NewRequest("DELETE", s.firestoreURL(docPath), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
