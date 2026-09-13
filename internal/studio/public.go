package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (handler *Handler) demo(writer http.ResponseWriter, request *http.Request) {
	if !handler.trustedWrite(request) {
		writeError(writer, fail(403, "Untrusted request origin."))
		return
	}
	if err := handler.limits.Take(request.Context(), "demo:"+handler.addressKey(request), 4, 15*time.Minute); err != nil {
		writer.Header().Set("Retry-After", "900")
		writeError(writer, err)
		return
	}
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Company  string `json:"company"`
		Workflow string `json:"workflow"`
		Consent  bool   `json:"consent"`
		Website  string `json:"website"`
	}
	if err := readJSON(writer, request, &input, 12*1024); err != nil {
		writeError(writer, err)
		return
	}
	for _, value := range []string{input.Name, input.Email, input.Company} {
		if strings.ContainsAny(value, "\r\n\x00") {
			writeError(writer, fail(400, "Check your contact details."))
			return
		}
	}
	input.Name, input.Email, input.Company, input.Workflow = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Email)), strings.TrimSpace(input.Company), strings.TrimSpace(input.Workflow)
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || !strings.Contains(strings.Split(input.Email, "@")[1], ".") || len(input.Email) > 254 || len(input.Name) < 2 || len(input.Name) > 100 || len(input.Company) < 2 || len(input.Company) > 160 || len(input.Workflow) < 10 || len(input.Workflow) > 2000 || !input.Consent || input.Website != "" || strings.ContainsRune(input.Workflow, 0) {
		writeError(writer, fail(400, "Complete your name, email, company, workflow, and consent."))
		return
	}
	if err := handler.limits.Take(request.Context(), "demo-email:"+hashKey(input.Email), 4, 15*time.Minute); err != nil {
		writer.Header().Set("Retry-After", "900")
		writeError(writer, err)
		return
	}
	identifier := uuid.NewString()
	record := Document{"id": identifier, "name": input.Name, "email": input.Email, "company": input.Company, "workflow": input.Workflow, "consent": true, "createdAt": time.Now().UnixMilli(), "status": "new", "source": "public-site"}
	if endpoint := handler.cfg.DemoWebhookURL; endpoint != "" {
		if !strings.HasPrefix(endpoint, "https://") || !safeEndpoint(endpoint) {
			writeError(writer, fail(503, "Demo delivery is not configured correctly."))
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 8*time.Second)
		defer cancel()
		encoded, _ := json.Marshal(record)
		upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
		if err != nil {
			writeError(writer, fail(503, "Demo delivery is unavailable."))
			return
		}
		upstream.Header.Set("Content-Type", "application/json")
		if handler.cfg.DemoWebhookSecret != "" {
			upstream.Header.Set("Authorization", "Bearer "+handler.cfg.DemoWebhookSecret)
		}
		response, err := handler.httpClient.Do(upstream)
		if err != nil {
			writeError(writer, fail(503, "Your request could not be delivered. Please try again shortly."))
			return
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			writeError(writer, fail(503, "Your request could not be delivered. Please try again shortly."))
			return
		}
	} else if err := handler.store.Set(request.Context(), "demoRequests/"+identifier, record, false); err != nil {
		writeError(writer, fail(503, "Your request could not be delivered. Please try again shortly."))
		return
	}
	writeJSON(writer, 201, Document{"ok": true})
}
