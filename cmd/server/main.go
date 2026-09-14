package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"t3z/api-gateway/internal/config"
	"t3z/api-gateway/internal/database"
	"t3z/api-gateway/internal/handlers"
	"t3z/api-gateway/internal/security"
	"t3z/api-gateway/internal/services"
	"t3z/api-gateway/internal/studio"
)

func main() {
	log.Println("Starting T3Z API Gateway (Go / Windmill Edition)...")

	cfg := config.LoadConfig()

	// 1. Initialize SQLite Database
	db, err := database.InitDB(cfg)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize database: %v", err)
	}
	defer db.Close()
	log.Println("Database initialized successfully.")

	// 2. Initialize Security Services
	jwtService, err := security.NewJWTService(cfg)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize JWT service: %v", err)
	}

	firebaseVerifier := security.NewFirebaseVerifier(cfg)

	// 3. Initialize Business Services
	windmillService := services.NewWindmillService(cfg)
	clientService := services.NewClientService(cfg, db, windmillService)
	firebaseService := services.NewFirebaseService(cfg)
	googleSheetsService, err := services.NewGoogleSheetsService(cfg)
	if err != nil {
		log.Printf("Warning: Google Sheets service initialization notice: %v", err)
	}

	// 4. Initialize Handlers
	authHandler := handlers.NewAuthHandler(cfg, db, jwtService, firebaseService)
	adminHandler := handlers.NewAdminHandler(db, clientService, firebaseVerifier)
	webhooksHandler := handlers.NewWebhooksHandler(db, jwtService, windmillService)
	googleSheetsHandler := handlers.NewGoogleSheetsHandler(cfg, db, jwtService, googleSheetsService)
	studioHandler, closeStudio, studioErr := studio.NewFirebaseHandler(context.Background(), cfg, db)
	if studioErr != nil {
		log.Printf("Studio API unavailable: %v", studioErr)
	} else {
		studioHandler.SetWorkflowTokens(jwtService.CreateWorkflowToken)
		defer closeStudio()
	}

	// 5. Setup Router
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// CORS configuration
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc:  func(r *http.Request, origin string) bool { return cfg.AllowsFrontendOrigin(origin) },
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Swagger UI and OpenAPI documentation - exclusively at /docs and /openapi.json
	r.Get("/docs", handlers.SwaggerUIHandler)
	r.Get("/docs/*", handlers.SwaggerUIHandler)
	r.Get("/openapi.json", handlers.OpenAPISpecHandler)

	// Health endpoint at root
	r.Get("/health", handlers.HealthHandler)

	// Root-level convenience aliases
	r.Route("/webhooks", func(wh chi.Router) {
		wh.HandleFunc("/{workflow_id}", webhooksHandler.Gateway)
	})
	r.Route("/auth", func(auth chi.Router) {
		auth.Post("/token", authHandler.IssueToken)
	})

	// Reusable API router for v1 (and backward-compatibility aliases)
	registerAPIRoutes := func(api chi.Router) {
		api.Get("/health", handlers.HealthHandler)

		// Auth endpoints
		api.Route("/auth", func(auth chi.Router) {
			auth.Post("/firebase-login", authHandler.FirebaseLogin)
			auth.Post("/token", authHandler.IssueToken)
		})

		// Admin endpoints
		api.Route("/admin", func(adm chi.Router) {
			adm.Use(adminHandler.RequireAdminMiddleware)
			adm.Post("/clients", adminHandler.CreateClient)
			adm.Post("/clients/{client_id}/rotate-secret", adminHandler.RotateSecret)
			adm.Post("/clients/{client_id}/workflows", adminHandler.CreateWorkflow)
		})

		// Webhook gateway endpoints
		api.Route("/webhooks", func(wh chi.Router) {
			wh.HandleFunc("/{workflow_id}", webhooksHandler.Gateway)
		})

		// Integrations
		api.Route("/integrations/google-sheets", func(gs chi.Router) {
			// Admin protected
			gs.Group(func(adm chi.Router) {
				adm.Use(adminHandler.RequireAdminMiddleware)
				adm.Post("/{client_id}/authorize", googleSheetsHandler.Authorize)
				adm.Get("/{client_id}", googleSheetsHandler.GetStatus)
				adm.Delete("/{client_id}", googleSheetsHandler.Disconnect)
			})

			// Public OAuth callback
			gs.Get("/callback", googleSheetsHandler.Callback)

			// Workflow Integration Token protected
			gs.Post("/values:batchGet", googleSheetsHandler.BatchGetValues)
		})

		// Studio endpoints
		api.Route("/studio", func(browser chi.Router) {
			if studioHandler != nil {
				studioHandler.Routes(browser)
			} else {
				browser.HandleFunc("/*", func(writer http.ResponseWriter, request *http.Request) {
					writer.Header().Set("Cache-Control", "no-store")
					http.Error(writer, "Studio API is not configured.", http.StatusServiceUnavailable)
				})
			}
		})
	}

	// /apis prefix
	r.Route("/apis", func(api chi.Router) {
		// Versioned APIs under /apis/v1
		api.Route("/v1", registerAPIRoutes)

		// Backward-compatible fallback for unversioned /apis/* callers
		registerAPIRoutes(api)
	})

	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Server listening on port %s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down server gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced shutdown: %v", err)
	}

	log.Println("Server exited successfully.")
}
