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
	flowPath := fmt.Sprintf("f/%s/%s", folderName, workflowID)

	baseURL := "https://api.t3z.in"
	if w.cfg != nil && w.cfg.PublicURL != "" {
		baseURL = w.cfg.PublicURL
	}
	webhookURL := fmt.Sprintf("%s/apis/v1/webhooks/%s", baseURL, workflowID)

	payload := map[string]interface{}{
		"path":        flowPath,
		"summary":     name,
		"description": fmt.Sprintf("T3Z Workflow for %s | Gateway Webhook URL: %s", name, webhookURL),
		"schema": map[string]interface{}{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type":    "object",
			"properties": map[string]interface{}{
				"args": map[string]interface{}{
					"type":        "object",
					"description": "Webhook args",
				},
				"payload": map[string]interface{}{
					"type":        "object",
					"description": "Webhook payload",
				},
				"t3z_context": map[string]interface{}{
					"type":        "object",
					"description": "T3Z Context",
				},
			},
		},
		"value": map[string]interface{}{
			"modules": []map[string]interface{}{
				{
					"id":      "a",
					"summary": name,
					"value": map[string]interface{}{
						"type": "rawscript",
						"content": fmt.Sprintf(`// T3Z Gateway provisioned workflow for %s
// Gateway Webhook URL: %s
// This flow is invoked securely by the API Gateway with verified credentials.
export async function main(args?: any, payload?: any) {
  return {
    success: true,
    workflow_id: "%s",
    timestamp: new Date().toISOString(),
    data: args || payload || {},
  };
}`, name, webhookURL, workflowID),
						"language": "bun",
						"input_transforms": map[string]interface{}{
							"args": map[string]interface{}{
								"type": "javascript",
								"expr": "flow_input.args",
							},
							"payload": map[string]interface{}{
								"type": "javascript",
								"expr": "flow_input.payload",
							},
						},
					},
				},
			},
		},
	}

	resp, body, err := w.request("POST", "flows/create", payload)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusConflict && !strings.Contains(string(body), "already exists") {
		return "", fmt.Errorf("Windmill flow create failed (%d): %s", resp.StatusCode, string(body))
	}

	return flowPath, nil
}

func (w *WindmillService) DeleteWorkflowScript(scriptPath string) {
	cleanPath := strings.TrimLeft(scriptPath, "/")
	_, _, _ = w.request("DELETE", "flows/delete/p/"+cleanPath, nil)
	_, _, _ = w.request("POST", "scripts/delete/p/"+cleanPath, nil)
}

func (w *WindmillService) ProxyWebhook(
	scriptPath string,
	method string,
	queryParams url.Values,
	body io.Reader,
	headers map[string]string,
) (*http.Response, error) {
	cleanPath := strings.TrimLeft(scriptPath, "/")

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	// 1. First attempt: execute as Windmill Flow (/jobs/run_wait_result/f/<path>)
	flowEndpoint := fmt.Sprintf("%s/api/w/%s/jobs/run_wait_result/f/%s",
		w.cfg.WindmillBaseURL,
		w.cfg.WindmillWorkspace,
		cleanPath,
	)

	if len(queryParams) > 0 {
		flowEndpoint += "?" + queryParams.Encode()
	}

	req, err := http.NewRequest(method, flowEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	if w.cfg.WindmillToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.cfg.WindmillToken)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// 2. If flow was not found (404), fall back to executing as a Script (/jobs/run_wait_result/p/<path>)
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		scriptEndpoint := fmt.Sprintf("%s/api/w/%s/jobs/run_wait_result/p/%s",
			w.cfg.WindmillBaseURL,
			w.cfg.WindmillWorkspace,
			cleanPath,
		)

		if len(queryParams) > 0 {
			scriptEndpoint += "?" + queryParams.Encode()
		}

		scriptReq, err := http.NewRequest(method, scriptEndpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}

		for k, v := range headers {
			scriptReq.Header.Set(k, v)
		}

		if w.cfg.WindmillToken != "" {
			scriptReq.Header.Set("Authorization", "Bearer "+w.cfg.WindmillToken)
		}

		return w.httpClient.Do(scriptReq)
	}

	return resp, nil
}
