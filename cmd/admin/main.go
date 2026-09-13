package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type ServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	ProjectID   string `json:"project_id"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run cmd/admin/main.go <firebase_uid>")
		os.Exit(1)
	}

	uid := os.Args[1]
	credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credPath == "" {
		fmt.Println("Error: GOOGLE_APPLICATION_CREDENTIALS environment variable is required.")
		os.Exit(1)
	}

	data, err := os.ReadFile(credPath)
	if err != nil {
		fmt.Printf("Error reading credentials file: %v\n", err)
		os.Exit(1)
	}

	var sa ServiceAccount
	if err := json.Unmarshal(data, &sa); err != nil {
		fmt.Printf("Error parsing service account JSON: %v\n", err)
		os.Exit(1)
	}

	privKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		fmt.Printf("Error parsing RSA private key: %v\n", err)
		os.Exit(1)
	}

	// Generate Service Account Bearer Token
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":   sa.ClientEmail,
		"sub":   sa.ClientEmail,
		"aud":   "https://oauth2.googleapis.com/token",
		"scope": "https://www.googleapis.com/auth/identitytoolkit",
		"iat":   now.Unix(),
		"exp":   now.Add(1 * time.Hour).Unix(),
	}

	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	assertion, err := t.SignedString(privKey)
	if err != nil {
		fmt.Printf("Error signing JWT assertion: %v\n", err)
		os.Exit(1)
	}

	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
	if err != nil {
		fmt.Printf("Error requesting OAuth token: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil || tr.AccessToken == "" {
		fmt.Println("Error: Failed to obtain access token from Google.")
		os.Exit(1)
	}

	// Set custom user claims in Firebase
	urlStr := fmt.Sprintf("https://identitytoolkit.googleapis.com/v1/projects/%s/accounts:update", sa.ProjectID)
	payload := map[string]interface{}{
		"localId":          uid,
		"customAttributes": "{\"admin\":true}",
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", urlStr, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	updateResp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("Error setting custom claims: %v\n", err)
		os.Exit(1)
	}
	defer updateResp.Body.Close()

	if updateResp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(updateResp.Body)
		fmt.Printf("Failed to set admin claim (%d): %s\n", updateResp.StatusCode, string(respBytes))
		os.Exit(1)
	}

	fmt.Printf("Admin claim set successfully for UID: %s\n", uid)
	fmt.Println("Sign out/in to obtain a fresh ID token.")
}
