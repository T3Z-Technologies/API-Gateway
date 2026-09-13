package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (handler *Handler) SetWorkflowTokens(issue func(string, string) (string, error)) {
	handler.workflowToken = issue
}

func containsEndpoint(value any, endpoint string) bool {
	modules, _ := value.([]any)
	for _, entry := range modules {
		module, ok := entry.(map[string]any)
		if !ok || module["enabled"] != true {
			continue
		}
		if text(module, "endpoint") == endpoint || containsEndpoint(module["children"], endpoint) {
			return true
		}
	}
	return false
}

func (handler *Handler) moduleData(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Endpoint string   `json:"endpoint"`
		OwnerUID string   `json:"ownerUid"`
		Payload  Document `json:"payload"`
		Preview  bool     `json:"preview"`
	}
	if err := readJSON(writer, request, &input, 64*1024); err != nil {
		writeError(writer, err)
		return
	}
	caller := current(request)
	uid, profile := caller.UID, caller.Profile
	if input.OwnerUID != "" && input.OwnerUID != uid {
		if caller.Role != "admin" || !validID(input.OwnerUID) {
			writeError(writer, fail(403, "This workspace is not accessible."))
			return
		}
		var err error
		uid = input.OwnerUID
		profile, err = handler.store.Get(request.Context(), "users/"+uid)
		if err != nil {
			writeError(writer, err)
			return
		}
	}
	if input.Preview && caller.Role != "admin" {
		writeError(writer, fail(403, "Only admins can preview endpoints."))
		return
	}
	if text(profile, "role") != "client" {
		writeError(writer, fail(400, "Select a client workspace."))
		return
	}
	if caller.Role != "admin" && text(profile, "status") == "paused" {
		writeError(writer, fail(403, "This workspace is paused."))
		return
	}
	if !input.Preview && !containsEndpoint(profile["modules"], input.Endpoint) {
		writeError(writer, fail(403, "This endpoint is not enabled for this workspace."))
		return
	}
	if input.Endpoint == "" || !safeEndpoint(input.Endpoint) {
		writeError(writer, fail(400, "Invalid module endpoint."))
		return
	}
	if err := handler.limits.Take(request.Context(), "modules:"+caller.UID, 120, time.Minute); err != nil {
		writeError(writer, err)
		return
	}
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil {
		writeError(writer, fail(400, "Invalid module endpoint."))
		return
	}
	clientID := text(profile, "gatewayClientId")
	if clientID == "" {
		clientID = uid
	}
	var token string
	if endpoint.IsAbs() {
		allowed := false
		for _, origin := range handler.cfg.ModuleOrigins {
			if origin == endpoint.Scheme+"://"+endpoint.Host {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(writer, fail(403, "This upstream is not in T3Z_MODULE_ORIGINS."))
			return
		}
		if handler.cfg.ModuleAPIToken == "" {
			writeError(writer, fail(503, "The upstream API credential is not configured."))
			return
		}
		token = handler.cfg.ModuleAPIToken
	} else {
		parts := strings.Split(endpoint.Path, "/")
		if len(parts) != 4 || parts[1] != "apis" || parts[2] != "webhooks" || !validID(parts[3]) {
			writeError(writer, fail(400, "Use /apis/webhooks/{workflow_id} for a gateway workflow."))
			return
		}
		if handler.workflowToken == nil {
			writeError(writer, fail(503, "Gateway workflows are not configured."))
			return
		}
		token, err = handler.workflowToken(clientID, parts[3])
		if err != nil {
			writeError(writer, err)
			return
		}
		endpoint.Scheme, endpoint.Host = "http", net.JoinHostPort("127.0.0.1", handler.cfg.Port)
	}
	if input.Payload == nil {
		input.Payload = Document{}
	}
	encoded, _ := json.Marshal(input.Payload)
	upstream, err := http.NewRequestWithContext(request.Context(), http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		writeError(writer, fail(400, "Invalid module endpoint."))
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	upstream.Header.Set("Authorization", "Bearer "+token)
	upstream.Header.Set("X-T3Z-User-Id", uid)
	upstream.Header.Set("X-T3Z-Client-Id", clientID)
	response, err := handler.httpClient.Do(upstream)
	if err != nil {
		writeError(writer, fail(502, "The module service is unavailable."))
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(writer, fail(502, "The module service rejected this request."))
		return
	}
	if response.StatusCode == http.StatusNoContent {
		writeJSON(writer, 200, Document{"ok": true})
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 || !json.Valid(body) {
		writeError(writer, fail(502, "The module returned an invalid or oversized response."))
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(200)
	_, _ = writer.Write(body)
}
