package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/google/uuid"
	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
)

func TestFirebaseEmulatorIntegration(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") != "127.0.0.1:8989" || os.Getenv("FIREBASE_AUTH_EMULATOR_HOST") != "127.0.0.1:9099" {
		t.Skip("requires local Auth and Firestore emulators on ports 9099 and 8989")
	}
	ctx := context.Background()
	origin := os.Getenv("STUDIO_BROWSER_TEST_URL")
	if origin == "" {
		origin = "http://127.0.0.1:3000"
	}
	cfg := &config.Config{
		DBPath:         filepath.Join(t.TempDir(), "test.db"),
		FirebaseAPIKey: "demo-api-key", FirebaseProjectID: "demo-t3z-pages",
		FrontendOrigins: []string{origin}, StudioCookieSecure: false, StudioCookieSameSite: "lax", StudioSessionHours: 24,
	}
	db, err := database.InitDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	handler, closeStore, err := NewFirebaseHandler(ctx, cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	marker := uuid.NewString()
	adminEmail, clientEmail := "admin-"+marker+"@example.test", "client-"+marker+"@example.test"
	adminUID, err := handler.identity.CreateUser(ctx, adminEmail, "emulator-test-password", "Studio Admin")
	if err != nil {
		t.Fatal(err)
	}
	clientUID, err := handler.identity.CreateUser(ctx, clientEmail, "emulator-test-password", "Studio Client")
	if err != nil {
		t.Fatal(err)
	}
	defer handler.identity.DeleteUser(ctx, adminUID)
	defer handler.identity.DeleteUser(ctx, clientUID)
	clientProfile := Document{
		"email": clientEmail, "displayName": "Studio Client", "companyName": "Test Company", "logoUrl": "", "role": "client", "status": "active", "plan": "starter", "planName": "Test Plan", "monthlyPrice": 1000, "autopayStatus": "pending", "onboardingComplete": true,
		"voice": Document{"included": 100, "addOn": 0, "used": 5}, "actions": Document{"included": 100, "addOn": 0, "used": 5}, "modules": []any{}, "createdAt": time.Now().UnixMilli(), "updatedAt": time.Now().UnixMilli(),
	}
	adminProfile := clone(clientProfile)
	adminProfile["role"], adminProfile["email"], adminProfile["displayName"] = "admin", adminEmail, "Studio Admin"
	if err := handler.store.Set(ctx, "users/"+adminUID, adminProfile, false); err != nil {
		t.Fatal(err)
	}
	if err := handler.store.Set(ctx, "users/"+clientUID, clientProfile, false); err != nil {
		t.Fatal(err)
	}
	store := handler.store.(*firestoreStore)
	bulk := store.client.BulkWriter(ctx)
	for index := 0; index < 399; index++ {
		profile := clone(clientProfile)
		profile["email"], profile["displayName"] = fmt.Sprintf("fixture-%d@example.test", index), fmt.Sprintf("Fixture Client %03d", index)
		if _, err := bulk.Set(store.client.Doc(fmt.Sprintf("users/fixture-%s-%03d", marker, index)), profile); err != nil {
			t.Fatal(err)
		}
	}
	bulk.End()
	defer func() {
		cleanup := store.client.BulkWriter(context.Background())
		_, _ = cleanup.Delete(store.client.Doc("users/" + adminUID))
		_, _ = cleanup.Delete(store.client.Doc("users/" + clientUID))
		for index := 0; index < 399; index++ {
			_, _ = cleanup.Delete(store.client.Doc(fmt.Sprintf("users/fixture-%s-%03d", marker, index)))
		}
		cleanup.End()
	}()
	router := chi.NewRouter()
	router.Use(cors.Handler(cors.Options{
		AllowOriginFunc: func(request *http.Request, origin string) bool { return cfg.AllowsFrontendOrigin(origin) },
		AllowedMethods:  []string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:  []string{"Accept", "Content-Type", "X-CSRF-Token"}, AllowCredentials: true,
	}))
	router.Route("/apis/studio", handler.Routes)
	server := httptest.NewUnstartedServer(router)
	if os.Getenv("STUDIO_BROWSER_TEST_SCRIPT") != "" {
		listener, err := net.Listen("tcp", "127.0.0.1:8789")
		if err != nil {
			t.Fatal(err)
		}
		_ = server.Listener.Close()
		server.Listener = listener
	}
	server.Start()
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 20 * time.Second}
	call := func(method, path string, input any) (int, []byte) {
		encoded, _ := json.Marshal(input)
		request, _ := http.NewRequest(method, server.URL+"/apis/studio"+path, bytes.NewReader(encoded))
		request.Header.Set("Origin", origin)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", "1")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, body
	}
	status, body := call("POST", "/auth/login", Document{"email": clientEmail, "password": "emulator-test-password"})
	if status != 200 {
		t.Fatalf("real session login failed: %d %s", status, body)
	}
	status, body = call("GET", "/auth/session", nil)
	if status != 200 {
		t.Fatalf("real session verification failed: %d %s", status, body)
	}
	status, _ = call("GET", "/users/"+adminUID, nil)
	if status != 403 {
		t.Fatal("real adapter allowed cross-tenant access")
	}
	status, body = call("POST", "/tickets", Document{"action": "create", "subject": "Emulator ticket", "message": "Verify the real transaction", "category": "question"})
	if status != 201 {
		t.Fatalf("real ticket transaction failed: %d %s", status, body)
	}
	status, body = call("POST", "/auth/login", Document{"email": adminEmail, "password": "emulator-test-password"})
	if status != 200 {
		t.Fatalf("admin session login failed: %d %s", status, body)
	}
	count, cursor := 0, ""
	for {
		path := "/users"
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		status, body = call("GET", path, nil)
		if status != 200 {
			t.Fatalf("pagination failed: %d %s", status, body)
		}
		var page struct {
			Items      []Document `json:"items"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 200 {
			t.Fatal("unbounded API page")
		}
		count += len(page.Items)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if count < 401 {
		t.Fatalf("expected 400 clients plus admin, got %d", count)
	}
	status, body = call("GET", "/tickets", nil)
	if status != 200 || !strings.Contains(string(body), "Emulator ticket") {
		t.Fatalf("real collection-group query failed: %d %s", status, body)
	}
	direct, err := http.Get("http://127.0.0.1:8989/v1/projects/demo-t3z-pages/databases/(default)/documents/users/" + clientUID)
	if err != nil {
		t.Fatal(err)
	}
	_ = direct.Body.Close()
	if direct.StatusCode != 403 {
		t.Fatalf("direct Firestore access was not denied: %d", direct.StatusCode)
	}
	if script := os.Getenv("STUDIO_BROWSER_TEST_SCRIPT"); script != "" {
		command := exec.Command("node", script)
		command.Env = append(os.Environ(), "SITE_TEST_URL="+origin, "STUDIO_TEST_ADMIN_EMAIL="+adminEmail, "STUDIO_TEST_CLIENT_EMAIL="+clientEmail, "STUDIO_TEST_CLIENT_UID="+clientUID, "STUDIO_TEST_API_URL="+server.URL+"/apis/studio")
		output, err := command.CombinedOutput()
		t.Log(string(output))
		if err != nil {
			t.Fatalf("static frontend integration failed: %v", err)
		}
	}
}
