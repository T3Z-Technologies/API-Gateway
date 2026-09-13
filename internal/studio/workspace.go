package studio

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func admin(request *http.Request) error {
	if current(request).Role != "admin" {
		return fail(403, "Admin access is required.")
	}
	return nil
}

func allowedFields(data Document, names ...string) error {
	for key := range data {
		allowed := false
		for _, name := range names {
			if key == name {
				allowed = true
				break
			}
		}
		if !allowed {
			return fail(400, "This field cannot be changed: "+key)
		}
	}
	return nil
}

func stringField(data Document, key string, maximum int, required bool) error {
	value, exists := data[key]
	if !exists && !required {
		return nil
	}
	content, ok := value.(string)
	if !ok || len(content) > maximum || strings.ContainsRune(content, 0) || (required && strings.TrimSpace(content) == "") {
		return fail(400, "Invalid "+key+".")
	}
	data[key] = strings.TrimSpace(content)
	return nil
}

func enumField(data Document, key string, choices ...string) error {
	if _, exists := data[key]; !exists {
		return nil
	}
	for _, choice := range choices {
		if text(data, key) == choice {
			return nil
		}
	}
	return fail(400, "Invalid "+key+".")
}

func nonnegativeField(data Document, key string) error {
	value, exists := data[key]
	if !exists {
		return nil
	}
	amount, ok := value.(float64)
	if !ok || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 1e12 {
		return fail(400, "Invalid "+key+".")
	}
	return nil
}

func safeEndpoint(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && parsed.User == nil && parsed.Fragment == "" && ((parsed.Scheme == "https" && parsed.Host != "") || (parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(parsed.Path, "/apis/") && !strings.Contains(parsed.Path, "..")))
}

func validateModules(value any, depth int) error {
	modules, ok := value.([]any)
	if !ok || len(modules) > 60 || depth > 2 {
		return fail(400, "Invalid dashboard modules.")
	}
	identifiers := make(map[string]bool)
	for _, entry := range modules {
		module, ok := entry.(map[string]any)
		if !ok || !validID(text(module, "id")) || identifiers[text(module, "id")] {
			return fail(400, "Module identifiers must be unique.")
		}
		identifiers[text(module, "id")] = true
		if err := stringField(module, "type", 64, true); err != nil {
			return err
		}
		if _, ok := module["enabled"].(bool); !ok {
			return fail(400, "Invalid module visibility.")
		}
		if !safeEndpoint(text(module, "endpoint")) {
			return fail(400, "Use an HTTPS or gateway module endpoint without URL credentials.")
		}
		if embed := text(module, "embedUrl"); embed != "" && (!strings.HasPrefix(embed, "https://") || !safeEndpoint(embed)) {
			return fail(400, "Use an HTTPS embed URL.")
		}
		if children, exists := module["children"]; exists {
			if err := validateModules(children, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProfile(data Document, isAdmin bool) error {
	fields := []string{"displayName", "companyName", "logoUrl", "industry", "onboardingComplete"}
	if isAdmin {
		fields = append(fields, "status", "plan", "planName", "monthlyPrice", "autopayStatus", "voice", "actions", "modules", "analyticsEndpoint", "gatewayClientId")
	}
	if err := allowedFields(data, fields...); err != nil {
		return err
	}
	for _, key := range []string{"displayName", "companyName", "industry", "planName"} {
		if err := stringField(data, key, 160, false); err != nil {
			return err
		}
	}
	for _, key := range []string{"logoUrl", "analyticsEndpoint"} {
		if err := stringField(data, key, 2048, false); err != nil {
			return err
		}
		if !safeEndpoint(text(data, key)) {
			return fail(400, "Invalid "+key+".")
		}
	}
	if value, exists := data["onboardingComplete"]; exists {
		if _, ok := value.(bool); !ok {
			return fail(400, "Invalid onboarding state.")
		}
	}
	for key, choices := range map[string][]string{"status": {"active", "paused"}, "plan": {"starter", "growth", "scale"}, "autopayStatus": {"active", "pending", "failed"}} {
		if err := enumField(data, key, choices...); err != nil {
			return err
		}
	}
	if err := nonnegativeField(data, "monthlyPrice"); err != nil {
		return err
	}
	if value, exists := data["gatewayClientId"]; exists {
		identifier, ok := value.(string)
		if !ok || (identifier != "" && !validID(identifier)) {
			return fail(400, "Invalid gateway client ID.")
		}
	}
	for _, key := range []string{"voice", "actions"} {
		if value, exists := data[key]; exists {
			meter, ok := value.(map[string]any)
			if !ok {
				return fail(400, "Invalid usage meter.")
			}
			if err := allowedFields(meter, "included", "addOn", "used", "ratePerMinute"); err != nil {
				return err
			}
			for _, field := range []string{"included", "addOn", "used"} {
				if _, exists := meter[field]; !exists {
					return fail(400, "Incomplete usage meter.")
				}
			}
			for field := range meter {
				if err := nonnegativeField(meter, field); err != nil {
					return err
				}
			}
		}
	}
	if modules, exists := data["modules"]; exists {
		return validateModules(modules, 0)
	}
	return nil
}

func (handler *Handler) listUsers(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	cursor := request.URL.Query().Get("cursor")
	if cursor != "" && !validID(cursor) {
		writeError(writer, fail(400, "Invalid cursor."))
		return
	}
	records, err := handler.store.List(request.Context(), Query{Collection: "users", Limit: 201, After: cursor})
	if err != nil {
		writeError(writer, err)
		return
	}
	var nextCursor any
	if len(records) > 200 {
		records = records[:200]
		nextCursor = records[len(records)-1].ID
	}
	items := make([]Document, 0, len(records))
	for _, record := range records {
		items = append(items, sessionData(record.ID, record.Data)["profile"].(Document))
	}
	writeJSON(writer, 200, Document{"items": items, "nextCursor": nextCursor})
}

func (handler *Handler) createUser(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	if err := handler.limits.Take(request.Context(), "users:"+current(request).UID, 30, time.Hour); err != nil {
		writeError(writer, err)
		return
	}
	var input struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if err := readJSON(writer, request, &input, 8192); err != nil {
		writeError(writer, err)
		return
	}
	input.Email = strings.TrimSpace(input.Email)
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 || len(input.Password) < 8 || len(input.Password) > 4096 || len(input.DisplayName) > 160 {
		writeError(writer, fail(400, "Use a valid email and a password of at least 8 characters."))
		return
	}
	uid, err := handler.identity.CreateUser(request.Context(), input.Email, input.Password, strings.TrimSpace(input.DisplayName))
	if err != nil {
		writeError(writer, err)
		return
	}
	now := time.Now().UnixMilli()
	name := strings.TrimSpace(input.DisplayName)
	if name == "" {
		name = strings.Split(input.Email, "@")[0]
	}
	profile := Document{"email": input.Email, "displayName": name, "companyName": "", "logoUrl": "", "role": "client", "status": "active", "plan": "starter", "planName": "", "monthlyPrice": 0, "autopayStatus": "pending", "voice": Document{"included": 0, "addOn": 0, "used": 0}, "actions": Document{"included": 0, "addOn": 0, "used": 0}, "modules": []any{}, "onboardingComplete": false, "createdAt": now, "updatedAt": now}
	if err := handler.store.Set(request.Context(), "users/"+uid, profile, false); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if cleanupErr := handler.identity.DeleteUser(cleanup, uid); cleanupErr != nil {
			log.Printf("Studio: account provisioning cleanup failed for uid %s", uid)
		}
		writeError(writer, err)
		return
	}
	writeJSON(writer, 201, Document{"ok": true, "uid": uid})
}

func (handler *Handler) updateProfile(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	var patch Document
	if err := readJSON(writer, request, &patch, 256*1024); err != nil {
		writeError(writer, err)
		return
	}
	if patch == nil {
		writeError(writer, fail(400, "Send a profile object."))
		return
	}
	if err := validateProfile(patch, current(request).Role == "admin"); err != nil {
		writeError(writer, err)
		return
	}
	patch["updatedAt"] = time.Now().UnixMilli()
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		data, err := transaction.Get("users/" + uid)
		if err != nil {
			return err
		}
		for key, value := range patch {
			data[key] = value
		}
		delete(data, "brandColor")
		return transaction.Set("users/"+uid, data, false)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) deleteUser(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	profile, err := handler.store.Get(request.Context(), "users/"+uid)
	if err != nil {
		writeError(writer, err)
		return
	}
	if text(profile, "role") != "client" {
		writeError(writer, fail(403, "Only client accounts can be removed here."))
		return
	}
	if err := handler.identity.DeleteUser(request.Context(), uid); err != nil {
		writeError(writer, err)
		return
	}
	if err := handler.store.Delete(request.Context(), "users/"+uid); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) listOwned(writer http.ResponseWriter, request *http.Request, collection, order string, maximum int) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	handler.list(writer, request, Query{Collection: "users/" + uid + "/" + collection, Order: order, Desc: true, Limit: maximum})
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request, query Query) {
	records, err := handler.store.List(request.Context(), query)
	if err != nil {
		writeError(writer, err)
		return
	}
	items := make([]Document, 0, len(records))
	for _, record := range records {
		item := withID(record)
		if query.Group {
			parts := strings.Split(record.Path, "/")
			if len(parts) != 4 || parts[0] != "users" || parts[2] != query.Collection {
				continue
			}
			item["uid"] = parts[1]
		}
		items = append(items, item)
	}
	writeJSON(writer, 200, items)
}

func (handler *Handler) listAgents(writer http.ResponseWriter, request *http.Request) {
	handler.listOwned(writer, request, "agents", "createdAt", 250)
}
func (handler *Handler) listCallLogs(writer http.ResponseWriter, request *http.Request) {
	handler.listOwned(writer, request, "call_logs", "timestamp", 100)
}
func (handler *Handler) listRequests(writer http.ResponseWriter, request *http.Request) {
	handler.listOwned(writer, request, "requests", "createdAt", 100)
}

func (handler *Handler) saveAgent(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	creating := request.Method == http.MethodPost
	if creating && current(request).Role != "admin" {
		writeError(writer, fail(403, "Admin access is required."))
		return
	}
	identifier := chi.URLParam(request, "id")
	if creating {
		identifier = uuid.NewString()
	}
	if !validID(identifier) {
		writeError(writer, fail(400, "Invalid agent."))
		return
	}
	var patch Document
	if err := readJSON(writer, request, &patch, 64*1024); err != nil {
		writeError(writer, err)
		return
	}
	if patch == nil {
		writeError(writer, fail(400, "Send an agent object."))
		return
	}
	fields := []string{"status"}
	if current(request).Role == "admin" {
		fields = append(fields, "name", "description", "model", "systemPrompt", "usage", "phoneNumber", "sarvamAgentId", "telephonyProvider")
	}
	if err := allowedFields(patch, fields...); err != nil {
		writeError(writer, err)
		return
	}
	for _, key := range []string{"name", "description", "model", "systemPrompt", "phoneNumber", "sarvamAgentId"} {
		maximum := 1000
		if key == "systemPrompt" {
			maximum = 30000
		}
		if err := stringField(patch, key, maximum, creating && key == "name"); err != nil {
			writeError(writer, err)
			return
		}
	}
	if err := enumField(patch, "status", "active", "inactive", "draft"); err != nil {
		writeError(writer, err)
		return
	}
	if err := enumField(patch, "telephonyProvider", "sarvam", "twilio"); err != nil {
		writeError(writer, err)
		return
	}
	if err := nonnegativeField(patch, "usage"); err != nil {
		writeError(writer, err)
		return
	}
	if text(patch, "status") == "active" && current(request).Role != "admin" && text(current(request).Profile, "status") == "paused" {
		writeError(writer, fail(403, "This workspace is paused."))
		return
	}
	now := time.Now().UnixMilli()
	patch["updatedAt"] = now
	path := "users/" + uid + "/agents/" + identifier
	if creating {
		if _, err := handler.store.Get(request.Context(), "users/"+uid); err != nil {
			writeError(writer, err)
			return
		}
		patch["createdAt"] = now
		if _, exists := patch["status"]; !exists {
			patch["status"] = "draft"
		}
		err = handler.store.Set(request.Context(), path, patch, false)
	} else {
		err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
			if _, err := transaction.Get(path); err != nil {
				return err
			}
			return transaction.Set(path, patch, true)
		})
	}
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true, "id": identifier})
}

func (handler *Handler) deleteAgent(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	uid, err := owner(request)
	identifier := chi.URLParam(request, "id")
	if err != nil {
		writeError(writer, err)
		return
	}
	if !validID(identifier) {
		writeError(writer, fail(400, "Invalid agent."))
		return
	}
	if err := handler.store.Delete(request.Context(), "users/"+uid+"/agents/"+identifier); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) allRequests(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	handler.list(writer, request, Query{Collection: "requests", Group: true, Order: "createdAt", Desc: true, Limit: 500})
}

func (handler *Handler) createRequest(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if current(request).Role != "client" {
		writeError(writer, fail(403, "Client access is required."))
		return
	}
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := readJSON(writer, request, &input, 16*1024); err != nil {
		writeError(writer, err)
		return
	}
	data := Document{"title": input.Title, "description": input.Description}
	if err := stringField(data, "title", 160, true); err != nil {
		writeError(writer, err)
		return
	}
	if err := stringField(data, "description", 10000, true); err != nil {
		writeError(writer, err)
		return
	}
	if err := handler.limits.Take(request.Context(), "requests:"+uid, 5, 10*time.Minute); err != nil {
		writeError(writer, err)
		return
	}
	now := time.Now().UnixMilli()
	data["status"], data["response"], data["clientUnread"], data["revision"] = "submitted", "", false, uuid.NewString()
	data["createdAt"], data["updatedAt"] = now, now
	identifier := uuid.NewString()
	if err := handler.store.Set(request.Context(), "users/"+uid+"/requests/"+identifier, data, false); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 201, Document{"ok": true, "id": identifier})
}

func (handler *Handler) respondToRequest(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	uid, err := owner(request)
	identifier := chi.URLParam(request, "id")
	if err != nil {
		writeError(writer, err)
		return
	}
	if !validID(identifier) {
		writeError(writer, fail(400, "Invalid request."))
		return
	}
	var input struct {
		Response         *string `json:"response"`
		Status           *string `json:"status"`
		ExpectedRevision *string `json:"expectedRevision"`
	}
	if err := readJSON(writer, request, &input, 16*1024); err != nil {
		writeError(writer, err)
		return
	}
	if input.ExpectedRevision == nil {
		writeError(writer, fail(400, "A revision is required."))
		return
	}
	patch := Document{}
	if input.Response != nil {
		patch["response"] = *input.Response
	}
	if input.Status != nil {
		patch["status"] = *input.Status
	}
	if err := stringField(patch, "response", 10000, false); err != nil {
		writeError(writer, err)
		return
	}
	if err := enumField(patch, "status", "submitted", "planned", "in_progress", "shipped", "declined"); err != nil {
		writeError(writer, err)
		return
	}
	path := "users/" + uid + "/requests/" + identifier
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		data, err := transaction.Get(path)
		if err != nil {
			return err
		}
		if text(data, "revision") != *input.ExpectedRevision {
			return fail(409, "This request changed. Reopen it and try again.")
		}
		now := time.Now().UnixMilli()
		if input.Response != nil && text(patch, "response") != text(data, "response") {
			patch["responseAt"] = now
		}
		patch["clientUnread"], patch["revision"], patch["updatedAt"] = true, uuid.NewString(), now
		return transaction.Set(path, patch, true)
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) readRequests(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	if current(request).UID != uid {
		writeError(writer, fail(403, "Only the owner can acknowledge these requests."))
		return
	}
	var input struct {
		Items []struct {
			ID       string `json:"id"`
			Revision string `json:"revision"`
		} `json:"items"`
	}
	if err := readJSON(writer, request, &input, 32*1024); err != nil {
		writeError(writer, err)
		return
	}
	if len(input.Items) > 100 {
		writeError(writer, fail(400, "Too many requests."))
		return
	}
	err = handler.store.Transact(request.Context(), func(transaction Transaction) error {
		paths := make([]string, 0, len(input.Items))
		for _, item := range input.Items {
			if !validID(item.ID) {
				return fail(400, "Invalid request.")
			}
			path := "users/" + uid + "/requests/" + item.ID
			data, err := transaction.Get(path)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if text(data, "revision") == item.Revision && data["clientUnread"] == true {
				paths = append(paths, path)
			}
		}
		for _, path := range paths {
			if err := transaction.Set(path, Document{"clientUnread": false}, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}

func (handler *Handler) listAnnouncements(writer http.ResponseWriter, request *http.Request) {
	handler.list(writer, request, Query{Collection: "announcements", Order: "createdAt", Desc: true, Limit: 100})
}

func (handler *Handler) createAnnouncement(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	var input struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := readJSON(writer, request, &input, 16*1024); err != nil {
		writeError(writer, err)
		return
	}
	data := Document{"title": input.Title, "body": input.Body, "createdAt": time.Now().UnixMilli()}
	if err := stringField(data, "title", 160, true); err != nil {
		writeError(writer, err)
		return
	}
	if err := stringField(data, "body", 10000, true); err != nil {
		writeError(writer, err)
		return
	}
	identifier := uuid.NewString()
	if err := handler.store.Set(request.Context(), "announcements/"+identifier, data, false); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 201, Document{"ok": true, "id": identifier})
}

func (handler *Handler) deleteAnnouncement(writer http.ResponseWriter, request *http.Request) {
	if err := admin(request); err != nil {
		writeError(writer, err)
		return
	}
	identifier := chi.URLParam(request, "id")
	if !validID(identifier) {
		writeError(writer, fail(400, "Invalid announcement."))
		return
	}
	if err := handler.store.Delete(request.Context(), "announcements/"+identifier); err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, 200, Document{"ok": true})
}
