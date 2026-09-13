package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"t3z/api-gateway/internal/config"
)

type WindmillService struct {
	cfg        *config.Config
	httpClient *http.Client
}

func NewWindmillService(cfg *config.Config) *WindmillService {
	return &WindmillService{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (w *WindmillService) request(method, path string, body interface{}) (*http.Response, []byte, error) {
	fullURL := fmt.Sprintf("%s/api/w/%s/%s", w.cfg.WindmillBaseURL, w.cfg.WindmillWorkspace, strings.TrimLeft(path, "/"))

	var bodyReader io.Reader
	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyReader = bytes.NewReader(v)
		case io.Reader:
			bodyReader = v
		default:
			jsonData, err := json.Marshal(body)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(jsonData)
		}
	}

	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return nil, nil, err
	}

	if w.cfg.WindmillToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.WindmillToken)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("could not connect to Windmill: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return resp, respBytes, nil
}

func (w *WindmillService) CreateFolder(name string) error {
	resp, body, err := w.request("POST", "folders/create", map[string]interface{}{
		"name":  name,
		"title": name,
	})
	if err != nil {
		return err
	}

	// 200/201 or 409 if already exists
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		return fmt.Errorf("Windmill folder create failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (w *WindmillService) DeleteFolder(name string) {
	_, _, _ = w.request("DELETE", "folders/delete/"+name, nil)
}

func (w *WindmillService) CreateWorkflowScript(folderName, workflowID, name string) (string, error) {
	scriptPath := fmt.Sprintf("f/%s/%s", folderName, workflowID)

	payload := map[string]interface{}{
		"path":        scriptPath,
		"summary":     name,
		"description": fmt.Sprintf("T3Z Workflow for %s", name),
		"content": fmt.Sprintf(`// T3Z Gateway provisioned workflow
export async function main(args?: any, payload?: any) {
  return {
    success: true,
    workflow_id: "%s",
    timestamp: new Date().toISOString(),
    data: args || payload || {},
  };
}`, workflowID),
		"language": "bun",
		"schema": map[string]interface{}{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type":    "object",
		},
	}

	resp, body, err := w.request("POST", "scripts/create", payload)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict {
		return "", fmt.Errorf("Windmill script create failed (%d): %s", resp.StatusCode, string(body))
	}

	return scriptPath, nil
}

func (w *WindmillService) DeleteWorkflowScript(scriptPath string) {
	_, _, _ = w.request("DELETE", "scripts/delete/"+scriptPath, nil)
}

func (w *WindmillService) ProxyWebhook(
	scriptPath string,
	method string,
	queryParams url.Values,
	body io.Reader,
	headers map[string]string,
) (*http.Response, error) {
	cleanPath := strings.TrimLeft(scriptPath, "/")
	endpoint := fmt.Sprintf("%s/api/w/%s/jobs/run_wait_result/p/%s",
		w.cfg.WindmillBaseURL,
		w.cfg.WindmillWorkspace,
		cleanPath,
	)

	if len(queryParams) > 0 {
		endpoint += "?" + queryParams.Encode()
	}

	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// Always ensure Windmill authorization token is present
	if w.cfg.WindmillToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.WindmillToken)
	}

	return w.httpClient.Do(req)
}
