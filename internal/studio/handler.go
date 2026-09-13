package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"t3z/api-gateway/internal/config"
)

type Handler struct {
	cfg           *config.Config
	store         Store
	identity      Identity
	limits        RateLimiter
	httpClient    *http.Client
	workflowToken func(string, string) (string, error)
}

type principal struct {
	UID     string
	Role    string
	Profile Document
}

type principalKey struct{}

func NewHandler(cfg *config.Config, store Store, identity Identity, limits RateLimiter) *Handler {
	return &Handler{cfg: cfg, store: store, identity: identity, limits: limits, httpClient: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(request *http.Request, previous []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (handler *Handler) Routes(router chi.Router) {
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(writer, request)
		})
	})
	router.Post("/auth/login", handler.login)
	router.Post("/auth/logout", handler.logout)
	router.Post("/demo", handler.demo)
	router.Post("/billing/deduct", handler.deduct)
	router.Post("/billing/razorpay-webhook", handler.paymentWebhook)
	router.Group(func(authenticated chi.Router) {
		authenticated.Use(handler.authenticate)
		authenticated.Get("/auth/session", handler.session)
		authenticated.Post("/billing/create-order", handler.createOrder)
		authenticated.Post("/modules/data", handler.moduleData)
		authenticated.Get("/users/{uid}", handler.profile)
		authenticated.Get("/users", handler.listUsers)
		authenticated.Post("/users", handler.createUser)
		authenticated.Patch("/users/{uid}", handler.updateProfile)
		authenticated.Delete("/users/{uid}", handler.deleteUser)
		authenticated.Get("/users/{uid}/agents", handler.listAgents)
		authenticated.Post("/users/{uid}/agents", handler.saveAgent)
		authenticated.Patch("/users/{uid}/agents/{id}", handler.saveAgent)
		authenticated.Delete("/users/{uid}/agents/{id}", handler.deleteAgent)
		authenticated.Get("/users/{uid}/call-logs", handler.listCallLogs)
		authenticated.Get("/users/{uid}/requests", handler.listRequests)
		authenticated.Post("/users/{uid}/requests", handler.createRequest)
		authenticated.Patch("/users/{uid}/requests/{id}", handler.respondToRequest)
		authenticated.Post("/users/{uid}/requests/read", handler.readRequests)
		authenticated.Get("/requests", handler.allRequests)
		authenticated.Post("/tickets", handler.ticketCommand)
		authenticated.Get("/tickets", handler.allTickets)
		authenticated.Get("/users/{uid}/tickets", handler.listTickets)
		authenticated.Get("/users/{uid}/tickets/{id}/messages", handler.ticketMessages)
		authenticated.Post("/users/{uid}/tickets/read", handler.readTickets)
		authenticated.Get("/announcements", handler.listAnnouncements)
		authenticated.Post("/announcements", handler.createAnnouncement)
		authenticated.Delete("/announcements/{id}", handler.deleteAnnouncement)
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, err error) {
	var problem *APIError
	if errors.As(err, &problem) {
		writeJSON(writer, problem.Status, Document{"error": problem.Message})
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeJSON(writer, http.StatusNotFound, Document{"error": "Not found."})
		return
	}
	writeJSON(writer, http.StatusInternalServerError, Document{"error": "The operation could not be completed."})
}

func readJSON(writer http.ResponseWriter, request *http.Request, value any, maximum int64) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fail(http.StatusUnsupportedMediaType, "Send an application/json request.")
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maximum)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			return fail(http.StatusRequestEntityTooLarge, "The request is too large.")
		}
		return fail(http.StatusBadRequest, "The request body is invalid.")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fail(http.StatusBadRequest, "Send one JSON object.")
	}
	return nil
}

func (handler *Handler) trustedWrite(request *http.Request) bool {
	return handler.cfg.AllowsFrontendOrigin(request.Header.Get("Origin")) && request.Header.Get("X-CSRF-Token") == "1"
}

func (handler *Handler) cookie(value string, remove bool) *http.Cookie {
	name := "__Host-t3z-session"
	if !handler.cfg.StudioCookieSecure {
		name = "t3z-session"
	}
	sameSite := http.SameSiteLaxMode
	if handler.cfg.StudioCookieSameSite == "none" {
		sameSite = http.SameSiteNoneMode
	}
	cookie := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: handler.cfg.StudioCookieSecure, SameSite: sameSite, MaxAge: handler.cfg.StudioSessionHours * 3600}
	if remove {
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
	}
	return cookie
}

func (handler *Handler) addressKey(request *http.Request) string {
	address, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		address = request.RemoteAddr
	}
	if handler.cfg.IsTrustedProxy(net.ParseIP(address)) {
		forwarded := strings.Split(request.Header.Get("X-Forwarded-For"), ",")
		for index := len(forwarded) - 1; index >= 0; index-- {
			candidate := net.ParseIP(strings.TrimSpace(forwarded[index]))
			if candidate == nil {
				break
			}
			address = candidate.String()
			if !handler.cfg.IsTrustedProxy(candidate) {
				break
			}
		}
	}
	return hashKey(address)
}

func hashKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func text(data Document, key string) string {
	value, _ := data[key].(string)
	return value
}

func number(data Document, key string) float64 {
	switch value := data[key].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	case int:
		return float64(value)
	default:
		return 0
	}
}

func validID(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "/\\\x00\r\n") && value != "." && value != ".."
}

func withID(record Record) Document {
	result := make(Document, len(record.Data)+1)
	for key, value := range record.Data {
		result[key] = value
	}
	result["id"] = record.ID
	return result
}

func current(request *http.Request) *principal {
	value, _ := request.Context().Value(principalKey{}).(*principal)
	return value
}

func owner(request *http.Request) (string, error) {
	uid := chi.URLParam(request, "uid")
	caller := current(request)
	if !validID(uid) {
		return "", fail(http.StatusBadRequest, "Invalid workspace.")
	}
	if caller.Role != "admin" && caller.UID != uid {
		return "", fail(http.StatusForbidden, "This workspace is not accessible.")
	}
	return uid, nil
}

func (handler *Handler) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead && !handler.trustedWrite(request) {
			writeError(writer, fail(http.StatusForbidden, "Untrusted request origin."))
			return
		}
		cookie, err := request.Cookie(handler.cookie("", false).Name)
		if err != nil {
			writeError(writer, fail(http.StatusUnauthorized, "Sign in to continue."))
			return
		}
		uid, err := handler.identity.Verify(request.Context(), cookie.Value)
		if err != nil || !validID(uid) {
			writeError(writer, fail(http.StatusUnauthorized, "Your session expired. Sign in again."))
			return
		}
		profile, err := handler.store.Get(request.Context(), "users/"+uid)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				err = fail(http.StatusUnauthorized, "This account is not provisioned.")
			}
			writeError(writer, err)
			return
		}
		role := text(profile, "role")
		if role != "admin" && role != "client" {
			writeError(writer, fail(http.StatusForbidden, "Account access is not configured."))
			return
		}
		caller := &principal{UID: uid, Role: role, Profile: profile}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), principalKey{}, caller)))
	})
}

func (handler *Handler) login(writer http.ResponseWriter, request *http.Request) {
	if !handler.trustedWrite(request) {
		writeError(writer, fail(http.StatusForbidden, "Untrusted request origin."))
		return
	}
	if err := handler.limits.Take(request.Context(), "login:"+handler.addressKey(request), 20, 15*time.Minute); err != nil {
		writeError(writer, err)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(writer, request, &input, 8192); err != nil {
		writeError(writer, err)
		return
	}
	input.Email = strings.TrimSpace(input.Email)
	if input.Email == "" || len(input.Email) > 254 || input.Password == "" || len(input.Password) > 4096 {
		writeError(writer, fail(http.StatusBadRequest, "Enter your email and password."))
		return
	}
	if err := handler.limits.Take(request.Context(), "login-email:"+hashKey(strings.ToLower(input.Email)), 10, 15*time.Minute); err != nil {
		writeError(writer, err)
		return
	}
	session, uid, err := handler.identity.Login(request.Context(), input.Email, input.Password)
	if err != nil || !validID(uid) {
		writeError(writer, fail(http.StatusUnauthorized, "Email or password is incorrect."))
		return
	}
	profile, err := handler.store.Get(request.Context(), "users/"+uid)
	if err != nil || (text(profile, "role") != "admin" && text(profile, "role") != "client") {
		writeError(writer, fail(http.StatusForbidden, "This account has not been provisioned."))
		return
	}
	http.SetCookie(writer, handler.cookie(session, false))
	writeJSON(writer, http.StatusOK, sessionData(uid, profile))
}

func (handler *Handler) logout(writer http.ResponseWriter, request *http.Request) {
	if !handler.trustedWrite(request) {
		writeError(writer, fail(http.StatusForbidden, "Untrusted request origin."))
		return
	}
	http.SetCookie(writer, handler.cookie("", true))
	writeJSON(writer, http.StatusOK, Document{"ok": true})
}

func sessionData(uid string, profile Document) Document {
	copy := withID(Record{ID: uid, Data: profile})
	delete(copy, "id")
	copy["uid"] = uid
	return Document{"user": Document{"uid": uid, "email": text(profile, "email")}, "profile": copy}
}

func (handler *Handler) session(writer http.ResponseWriter, request *http.Request) {
	caller := current(request)
	writeJSON(writer, http.StatusOK, sessionData(caller.UID, caller.Profile))
}

func (handler *Handler) profile(writer http.ResponseWriter, request *http.Request) {
	uid, err := owner(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	data, err := handler.store.Get(request.Context(), "users/"+uid)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, sessionData(uid, data)["profile"])
}
