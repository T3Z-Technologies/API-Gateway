package models

type FirebaseLoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type FirebaseLoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

type CreateClientRequest struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
}

type CreateClientResponse struct {
	ClientID     string `json:"client_id"`
	ClientName   string `json:"client_name"`
	ClientSecret string `json:"client_secret"`
}

type CreateWorkflowRequest struct {
	Name string `json:"name"`
}

type WorkflowResponse struct {
	ClientID     string `json:"client_id"`
	WorkflowID   string `json:"workflow_id"`
	WindmillPath string `json:"windmill_path"`
	Name         string `json:"name"`
	WebhookURL   string `json:"webhook_url"`
	Status       string `json:"status"`
}

type TokenRequest struct {
	WorkflowID string `json:"workflow_id"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	WorkflowID  string `json:"workflow_id"`
}

type GoogleAuthorizationResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresIn        int    `json:"expires_in"`
}

type GoogleConnectionResponse struct {
	ClientID  string   `json:"client_id"`
	Connected bool     `json:"connected"`
	Email     *string  `json:"email"`
	Scopes    []string `json:"scopes"`
}

type GoogleSheetsBatchGetRequest struct {
	SpreadsheetID  string   `json:"spreadsheet_id"`
	Ranges         []string `json:"ranges"`
	MajorDimension string   `json:"major_dimension"`
}

type HealthResponse struct {
	Status string `json:"status"`
}

type ErrorResponse struct {
	Detail string `json:"detail"`
}
