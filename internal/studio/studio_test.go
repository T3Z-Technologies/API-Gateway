package studio

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"t3z/api-gateway/internal/config"
)

type memoryStore struct {
	mu        sync.Mutex
	documents map[string]Document
}

func clone(data Document) Document {
	encoded, _ := json.Marshal(data)
	var copy Document
	_ = json.Unmarshal(encoded, &copy)
	return copy
}

func (store *memoryStore) Get(ctx context.Context, path string) (Document, error) {
	data, exists := store.documents[path]
	if !exists {
		return nil, ErrNotFound
	}
	return clone(data), nil
}

func (store *memoryStore) List(ctx context.Context, query Query) ([]Record, error) {
	records := make([]Record, 0)
	for path, data := range store.documents {
		parts := strings.Split(path, "/")
		collection := strings.Join(parts[:len(parts)-1], "/")
		if (!query.Group && collection != query.Collection) || (query.Group && parts[len(parts)-2] != query.Collection) {
			continue
		}
		identifier := parts[len(parts)-1]
		if query.After != "" && identifier <= query.After {
			continue
		}
		if query.Order != "" {
			if _, exists := data[query.Order]; !exists {
				continue
			}
		}
		records = append(records, Record{ID: identifier, Path: path, Data: clone(data)})
	}
	sort.Slice(records, func(left, right int) bool {
		if query.Order == "" {
			return records[left].ID < records[right].ID
		}
		if query.Desc {
			return number(records[left].Data, query.Order) > number(records[right].Data, query.Order)
		}
		return number(records[left].Data, query.Order) < number(records[right].Data, query.Order)
	})
	if query.Limit > 0 && len(records) > query.Limit {
		records = records[:query.Limit]
	}
	return records, nil
}

func (store *memoryStore) Set(ctx context.Context, path string, data Document, merge bool) error {
	if !merge || store.documents[path] == nil {
		store.documents[path] = Document{}
	}
	for key, value := range clone(data) {
		store.documents[path][key] = value
	}
	return nil
}

func (store *memoryStore) Delete(ctx context.Context, path string) error {
	delete(store.documents, path)
	return nil
}

type memoryTransaction struct{ store *memoryStore }

func (transaction memoryTransaction) Get(path string) (Document, error) {
	return transaction.store.Get(context.Background(), path)
}
func (transaction memoryTransaction) Set(path string, data Document, merge bool) error {
	return transaction.store.Set(context.Background(), path, data, merge)
}
func (transaction memoryTransaction) Delete(path string) error {
	return transaction.store.Delete(context.Background(), path)
}
func (store *memoryStore) Transact(ctx context.Context, action func(Transaction) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	backup := make(map[string]Document)
	for path, data := range store.documents {
		backup[path] = clone(data)
	}
	if err := action(memoryTransaction{store}); err != nil {
		store.documents = backup
		return err
	}
	return nil
}

type fakeIdentity struct{}

func (identity fakeIdentity) Login(ctx context.Context, email, password string) (string, string, error) {
	if email == "client@example.com" && password == "test-password" {
		return "client-session", "client", nil
	}
	return "", "", errors.New("invalid credentials")
}
func (identity fakeIdentity) Verify(ctx context.Context, session string) (string, error) {
	if session == "client-session" {
		return "client", nil
	}
	if session == "admin-session" {
		return "admin", nil
	}
	return "", errors.New("invalid session")
}
func (identity fakeIdentity) CreateUser(ctx context.Context, email, password, name string) (string, error) {
	return "created-client", nil
}
func (identity fakeIdentity) DeleteUser(ctx context.Context, uid string) error { return nil }

type fakeLimits struct{}

func (limits fakeLimits) Take(ctx context.Context, key string, maximum int, duration time.Duration) error {
	return nil
}

func testHandler() (*Handler, *memoryStore, http.Handler) {
	store := &memoryStore{documents: map[string]Document{
		"users/client": {"email": "client@example.com", "role": "client", "status": "active"},
		"users/admin":  {"email": "admin@example.com", "role": "admin", "status": "active"},
		"users/other":  {"email": "other@example.com", "role": "client", "status": "active"},
	}}
	cfg := &config.Config{FrontendOrigins: []string{"https://studio.example.com"}, StudioCookieSecure: true, StudioSessionHours: 24}
	handler := NewHandler(cfg, store, fakeIdentity{}, fakeLimits{})
	router := chi.NewRouter()
	router.Route("/apis/studio", handler.Routes)
	return handler, store, router
}

func studioRequest(router http.Handler, method, path, body, session string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/apis/studio"+path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://studio.example.com")
	request.Header.Set("X-CSRF-Token", "1")
	if session != "" {
		request.AddCookie(&http.Cookie{Name: "__Host-t3z-session", Value: session})
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestSessionAndTenantBoundary(t *testing.T) {
	_, _, router := testHandler()
	for _, test := range []struct {
		path, session string
		status        int
	}{
		{"/auth/session", "", 401},
		{"/auth/session", "invalid", 401},
		{"/auth/session", "client-session", 200},
		{"/users/client", "client-session", 200},
		{"/users/other", "client-session", 403},
		{"/users/other", "admin-session", 200},
	} {
		response := studioRequest(router, "GET", test.path, "", test.session)
		if response.Code != test.status {
			t.Fatalf("%s (%s): got %d: %s", test.path, test.session, response.Code, response.Body)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private responses must not be cached")
		}
	}
}

func TestLoginCookieAndLogout(t *testing.T) {
	_, _, router := testHandler()
	response := studioRequest(router, "POST", "/auth/login", `{"email":"client@example.com","password":"test-password"}`, "")
	if response.Code != 200 {
		t.Fatalf("login failed: %s", response.Body)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Domain != "" || cookies[0].Path != "/" {
		t.Fatal("session cookie protections are missing")
	}
	if strings.Contains(response.Body.String(), "client-session") {
		t.Fatal("session secret leaked in JSON")
	}
	response = studioRequest(router, "POST", "/auth/logout", `{}`, "client-session")
	if response.Code != 200 || response.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear the session cookie")
	}
}

func TestLoginRejectsCSRFAndInvalidBody(t *testing.T) {
	_, _, router := testHandler()
	for _, origin := range []string{"", "https://untrusted.example.com"} {
		request := httptest.NewRequest("POST", "/apis/studio/auth/login", strings.NewReader(`{}`))
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", "1")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 403 {
			t.Fatal("untrusted login origin was accepted")
		}
	}
	response := studioRequest(router, "POST", "/auth/login", `{"email":"client@example.com","password":"wrong"}`, "")
	if response.Code != 401 {
		t.Fatal("invalid password was accepted")
	}
	response = studioRequest(router, "POST", "/auth/login", `{"email":"client@example.com","password":"test-password","role":"admin"}`, "")
	if response.Code != 400 {
		t.Fatal("unexpected authentication fields were accepted")
	}
}

func TestProfileAndAdminWritePermissions(t *testing.T) {
	_, store, router := testHandler()
	for _, body := range []string{`{"role":"admin"}`, `{"voice":{"included":999,"addOn":0,"used":0}}`, `{"modules":[]}`, `{"monthlyPrice":0}`} {
		response := studioRequest(router, "PATCH", "/users/client", body, "client-session")
		if response.Code != 400 {
			t.Fatalf("privileged profile patch accepted: %s", body)
		}
	}
	response := studioRequest(router, "PATCH", "/users/other", `{"displayName":"Changed"}`, "client-session")
	if response.Code != 403 {
		t.Fatal("cross-tenant write accepted")
	}
	response = studioRequest(router, "PATCH", "/users/client", `{"displayName":"Updated","onboardingComplete":true}`, "client-session")
	if response.Code != 200 || store.documents["users/client"]["displayName"] != "Updated" {
		t.Fatalf("own profile update failed: %s", response.Body)
	}
	for _, path := range []string{"/users", "/announcements", "/users/client/agents"} {
		response = studioRequest(router, "POST", path, `{}`, "client-session")
		if response.Code != 403 {
			t.Fatalf("client accessed admin mutation %s: %d", path, response.Code)
		}
	}
	response = studioRequest(router, "POST", "/users", `{"email":"new@example.com","password":"test-password","displayName":"New client"}`, "admin-session")
	if response.Code != 201 || store.documents["users/created-client"]["role"] != "client" {
		t.Fatalf("admin creation failed: %s", response.Body)
	}
}

func TestRequestRevisionAndReadReceipt(t *testing.T) {
	_, store, router := testHandler()
	store.documents["users/client/requests/request"] = Document{"title": "Feature", "revision": "first", "response": "", "clientUnread": true}
	response := studioRequest(router, "PATCH", "/users/client/requests/request", `{"response":"Ready","status":"shipped","expectedRevision":"stale"}`, "admin-session")
	if response.Code != 409 {
		t.Fatal("stale request update accepted")
	}
	response = studioRequest(router, "PATCH", "/users/client/requests/request", `{"response":"Ready","status":"shipped","expectedRevision":"first"}`, "admin-session")
	if response.Code != 200 {
		t.Fatalf("request update failed: %s", response.Body)
	}
	response = studioRequest(router, "POST", "/users/client/requests/read", `{"items":[{"id":"request","revision":"first"}]}`, "client-session")
	if response.Code != 200 || store.documents["users/client/requests/request"]["clientUnread"] != true {
		t.Fatal("stale read receipt cleared a newer update")
	}
}

func TestAgentFieldRestrictions(t *testing.T) {
	_, store, router := testHandler()
	store.documents["users/client/agents/agent"] = Document{"name": "Agent", "status": "active"}
	response := studioRequest(router, "PATCH", "/users/client/agents/agent", `{"systemPrompt":"changed"}`, "client-session")
	if response.Code != 400 {
		t.Fatal("client modified agent configuration")
	}
	response = studioRequest(router, "PATCH", "/users/client/agents/agent", `{"status":"inactive"}`, "client-session")
	if response.Code != 200 || store.documents["users/client/agents/agent"]["status"] != "inactive" {
		t.Fatalf("agent pause failed: %s", response.Body)
	}
	response = studioRequest(router, "PATCH", "/users/other/agents/agent", `{"status":"active"}`, "client-session")
	if response.Code != 403 {
		t.Fatal("cross-tenant agent update accepted")
	}
}

func TestTicketConversationAndConcurrency(t *testing.T) {
	_, store, router := testHandler()
	response := studioRequest(router, "POST", "/tickets", `{"action":"create","subject":"Help","message":"Please review","category":"question"}`, "client-session")
	if response.Code != 201 {
		t.Fatalf("ticket creation failed: %s", response.Body)
	}
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	path := "users/client/tickets/" + result.ID
	command := `{"action":"message","ownerUid":"client","ticketId":"` + result.ID + `","expectedVersion":0,"message":"Answered","status":"resolved"}`
	response = studioRequest(router, "POST", "/tickets", command, "admin-session")
	if response.Code != 200 || store.documents[path]["status"] != "resolved" || store.documents[path]["clientUnread"] != true {
		t.Fatalf("admin reply failed: %s", response.Body)
	}
	response = studioRequest(router, "POST", "/tickets", command, "admin-session")
	if response.Code != 409 {
		t.Fatal("duplicate/stale message was accepted")
	}
	response = studioRequest(router, "POST", "/tickets", `{"action":"message","ticketId":"`+result.ID+`","expectedVersion":1,"message":"One more question"}`, "client-session")
	if response.Code != 200 || store.documents[path]["status"] != "open" || store.documents[path]["adminUnread"] != true {
		t.Fatalf("client reply did not reopen ticket: %s", response.Body)
	}
	response = studioRequest(router, "POST", "/users/client/tickets/read", `{"items":[{"id":"`+result.ID+`","version":1}]}`, "admin-session")
	if response.Code != 200 || store.documents[path]["adminUnread"] != true {
		t.Fatal("stale read cleared a new message")
	}
	response = studioRequest(router, "POST", "/tickets", `{"action":"message","ownerUid":"other","ticketId":"`+result.ID+`","expectedVersion":2,"message":"Unauthorized"}`, "client-session")
	if response.Code != 403 {
		t.Fatal("forged ticket owner was accepted")
	}
	store.documents[path]["messageCount"] = 100
	response = studioRequest(router, "POST", "/tickets", `{"action":"message","ticketId":"`+result.ID+`","expectedVersion":2,"message":"Too many"}`, "client-session")
	if response.Code != 409 {
		t.Fatal("message cap was not enforced")
	}
}

func TestTicketCreationRateLimit(t *testing.T) {
	_, _, router := testHandler()
	for attempt := 0; attempt < 6; attempt++ {
		response := studioRequest(router, "POST", "/tickets", `{"action":"create","subject":"Help","message":"Details","category":"question"}`, "client-session")
		want := 201
		if attempt == 5 {
			want = 429
		}
		if response.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", attempt, response.Code, want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (transport roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func signedRequest(router http.Handler, path, body, header, secret string) *httptest.ResponseRecorder {
	request := httptest.NewRequest("POST", "/apis/studio"+path, strings.NewReader(body))
	signature := hmac.New(sha256.New, []byte(secret))
	_, _ = signature.Write([]byte(body))
	request.Header.Set(header, hex.EncodeToString(signature.Sum(nil)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestBillingPriceAndPaymentReplay(t *testing.T) {
	handler, store, router := testHandler()
	handler.cfg.RazorpayKeyID, handler.cfg.RazorpayKeySecret, handler.cfg.RazorpayWebhookSecret = "test-key", "test-secret", "webhook-secret"
	store.documents["users/client"]["voice"] = Document{"included": 0, "addOn": 0, "used": 0}
	handler.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var order Document
		_ = json.NewDecoder(request.Body).Decode(&order)
		if number(order, "amount") != 75000 {
			t.Fatal("credit pack price was not computed server-side")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"order_test","amount":75000,"currency":"INR"}`)), Header: make(http.Header)}, nil
	})
	response := studioRequest(router, "POST", "/billing/create-order", `{"packId":"pack-150","amount":1}`, "client-session")
	if response.Code != 400 {
		t.Fatal("client-supplied amount accepted")
	}
	response = studioRequest(router, "POST", "/billing/create-order", `{"packId":"pack-150"}`, "client-session")
	if response.Code != 200 {
		t.Fatalf("order creation failed: %s", response.Body)
	}
	event := `{"event":"order.paid","payload":{"order":{"entity":{"id":"order_test","amount":75000,"currency":"INR","status":"paid"}}}}`
	response = signedRequest(router, "/billing/razorpay-webhook", event, "X-Razorpay-Signature", "wrong-secret")
	if response.Code != 401 {
		t.Fatal("invalid payment signature accepted")
	}
	for attempt := 0; attempt < 2; attempt++ {
		response = signedRequest(router, "/billing/razorpay-webhook", event, "X-Razorpay-Signature", "webhook-secret")
		if response.Code != 200 {
			t.Fatalf("payment event failed: %s", response.Body)
		}
	}
	voice := store.documents["users/client"]["voice"].(map[string]any)
	if number(voice, "addOn") != 150 {
		t.Fatal("payment replay credited the account twice")
	}
}

func TestUsageDeductionIsAtomicAndIdempotent(t *testing.T) {
	handler, store, router := testHandler()
	handler.cfg.BillingWebhookSecret = "usage-secret"
	store.documents["users/client"]["voice"] = Document{"included": 1, "addOn": 0, "used": 0}
	store.documents["users/client/agents/agent"] = Document{"status": "active"}
	for attempt := 0; attempt < 2; attempt++ {
		response := signedRequest(router, "/billing/deduct", `{"uid":"client","eventId":"call-1","durationSeconds":120}`, "X-Webhook-Signature", "usage-secret")
		if response.Code != 200 {
			t.Fatalf("deduction failed: %s", response.Body)
		}
	}
	voice := store.documents["users/client"]["voice"].(map[string]any)
	if number(voice, "used") != 2 || store.documents["users/client/agents/agent"]["status"] != "inactive" {
		t.Fatal("deduction replay or agent shutdown failed")
	}
	response := signedRequest(router, "/billing/deduct", `{"uid":"client","eventId":"call-2","minutes":-100}`, "X-Webhook-Signature", "usage-secret")
	if response.Code != 400 {
		t.Fatal("negative usage accepted")
	}
	response = signedRequest(router, "/billing/deduct", `{"uid":"client","minutes":1}`, "X-Webhook-Signature", "usage-secret")
	if response.Code != 400 {
		t.Fatal("non-idempotent usage event accepted")
	}
}

func TestDemoDeliveryAndFailure(t *testing.T) {
	handler, store, router := testHandler()
	body := `{"name":"Test User","email":"test@example.com","company":"Test Company","workflow":"Handle incoming customer enquiries","consent":true,"website":""}`
	response := studioRequest(router, "POST", "/demo", body, "")
	if response.Code != 201 {
		t.Fatalf("demo request failed: %s", response.Body)
	}
	leads, _ := store.List(context.Background(), Query{Collection: "demoRequests"})
	if len(leads) != 1 {
		t.Fatal("success returned without persisting the demo")
	}
	handler.cfg.DemoWebhookURL = "https://demo.example.com/lead"
	handler.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	response = studioRequest(router, "POST", "/demo", body, "")
	if response.Code != 503 {
		t.Fatal("failed demo delivery returned success")
	}
	response = studioRequest(router, "POST", "/demo", strings.Replace(body, `"website":""`, `"website":"spam"`, 1), "")
	if response.Code != 400 {
		t.Fatal("honeypot submission accepted")
	}
}

func TestDemoRequestBoundary(t *testing.T) {
	_, store, router := testHandler()
	for _, test := range []struct {
		name, origin, contentType, body string
		status                          int
	}{
		{"untrusted", "https://untrusted.example", "application/json", `{}`, 403},
		{"missing origin", "", "application/json", `{}`, 403},
		{"form body", "https://studio.example.com", "application/x-www-form-urlencoded", "name=test", 415},
		{"malformed", "https://studio.example.com", "application/json", `{`, 400},
		{"empty fields", "https://studio.example.com", "application/json", `{}`, 400},
		{"oversized", "https://studio.example.com", "application/json", `{"workflow":"` + strings.Repeat("x", 14000) + `"}`, 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/apis/studio/demo", strings.NewReader(test.body))
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("X-CSRF-Token", "1")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("got %d, want %d", response.Code, test.status)
			}
		})
	}
	leads, _ := store.List(context.Background(), Query{Collection: "demoRequests"})
	if len(leads) != 0 {
		t.Fatal("invalid submissions were persisted")
	}
}

func TestModuleProxyAuthorization(t *testing.T) {
	handler, store, router := testHandler()
	handler.cfg.ModuleOrigins, handler.cfg.ModuleAPIToken = []string{"https://data.example.com"}, "server-only-secret"
	store.documents["users/client"]["modules"] = []any{Document{"id": "table", "enabled": true, "endpoint": "https://data.example.com/records"}}
	called := 0
	handler.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		called++
		if request.Header.Get("Authorization") != "Bearer server-only-secret" || request.Header.Get("X-T3Z-Client-Id") != "client" || request.Header.Get("Cookie") != "" {
			t.Fatal("upstream credentials or trusted tenant header are incorrect")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"name":"Record"}]`))}, nil
	})
	for _, body := range []string{
		`{"endpoint":"https://data.example.com/other"}`,
		`{"endpoint":"https://untrusted.example.com/records"}`,
		`{"endpoint":"https://data.example.com/records","ownerUid":"other"}`,
		`{"endpoint":"https://data.example.com/records","preview":true}`,
	} {
		response := studioRequest(router, "POST", "/modules/data", body, "client-session")
		if response.Code != 403 {
			t.Fatalf("unauthorized module request accepted: %s", body)
		}
	}
	if called != 0 {
		t.Fatal("unauthorized requests reached upstream")
	}
	response := studioRequest(router, "POST", "/modules/data", `{"endpoint":"https://data.example.com/records"}`, "client-session")
	if response.Code != 200 || called != 1 {
		t.Fatalf("module fetch failed: %s", response.Body)
	}
	store.documents["users/client"]["modules"] = []any{Document{"id": "table", "enabled": false, "endpoint": "https://data.example.com/records"}}
	response = studioRequest(router, "POST", "/modules/data", `{"endpoint":"https://data.example.com/records"}`, "client-session")
	if response.Code != 403 {
		t.Fatal("disabled module was accessible")
	}
}

func TestForwardedAddressRequiresTrustedProxy(t *testing.T) {
	handler, _, _ := testHandler()
	request := httptest.NewRequest("POST", "/", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "198.51.100.2, 203.0.113.9")
	if handler.addressKey(request) != hashKey("127.0.0.1") {
		t.Fatal("untrusted forwarded address was used")
	}
	_, network, _ := net.ParseCIDR("127.0.0.1/32")
	handler.cfg.TrustedProxies = []*net.IPNet{network}
	if handler.addressKey(request) != hashKey("203.0.113.9") {
		t.Fatal("did not select the nearest untrusted hop")
	}
}
