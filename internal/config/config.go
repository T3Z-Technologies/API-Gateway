package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	BaseDir                    string
	DBPath                     string
	Port                       string
	PrivateKeyPath             string
	PublicKeyPath              string
	JWTIssuer                  string
	JWTAudience                string
	JWTTTLSeconds              int
	FirebaseAPIKey             string
	FirebaseCredentials        string
	WindmillBaseURL            string
	WindmillWorkspace          string
	WindmillToken              string
	GoogleOAuthClientID        string
	GoogleOAuthClientSecret    string
	GoogleOAuthRedirectURI     string
	GoogleOAuthSuccessRedirect string
	GoogleTokenEncryptionKey   string
	IntegrationJWTAudience     string
	IntegrationJWTTTLSeconds   int
	FrontendOrigins            []string
	StudioCookieSecure         bool
	StudioCookieSameSite       string
	StudioSessionHours         int
	FirebaseProjectID          string
	RazorpayKeyID              string
	RazorpayKeySecret          string
	RazorpayWebhookSecret      string
	BillingWebhookSecret       string
	DemoWebhookURL             string
	DemoWebhookSecret          string
	ModuleOrigins              []string
	ModuleAPIToken             string
	TrustedProxies             []*net.IPNet
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

func LoadConfig() *Config {
	pwd, err := os.Getwd()
	if err != nil {
		pwd = "."
	}

	return &Config{
		BaseDir: pwd,
		DBPath:  getEnv("T3Z_DB_PATH", filepath.Join(pwd, "data", "auth.db")),
		Port:    getEnv("T3Z_PORT", getEnv("PORT", "8080")),

		PrivateKeyPath: getEnv("T3Z_JWT_PRIVATE_KEY", filepath.Join(pwd, "keys", "private.pem")),
		PublicKeyPath:  getEnv("T3Z_JWT_PUBLIC_KEY", filepath.Join(pwd, "keys", "public.pem")),
		JWTIssuer:      getEnv("T3Z_JWT_ISSUER", "t3z.in"),
		JWTAudience:    getEnv("T3Z_JWT_AUDIENCE", "t3z-api"),
		JWTTTLSeconds:  getEnvInt("T3Z_JWT_TTL_SECONDS", 3600),

		FirebaseAPIKey:      os.Getenv("T3Z_FIREBASE_API_KEY"),
		FirebaseCredentials: os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),

		WindmillBaseURL:   strings.TrimRight(getEnv("WINDMILL_BASE_URL", "http://127.0.0.1:8000"), "/"),
		WindmillWorkspace: getEnv("WINDMILL_WORKSPACE", "main"),
		WindmillToken:     os.Getenv("WINDMILL_TOKEN"),

		GoogleOAuthClientID:        os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
		GoogleOAuthClientSecret:    os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		GoogleOAuthRedirectURI:     os.Getenv("GOOGLE_OAUTH_REDIRECT_URI"),
		GoogleOAuthSuccessRedirect: os.Getenv("GOOGLE_OAUTH_SUCCESS_REDIRECT"),
		GoogleTokenEncryptionKey:   os.Getenv("GOOGLE_TOKEN_ENCRYPTION_KEY"),

		IntegrationJWTAudience:   getEnv("T3Z_INTEGRATION_JWT_AUDIENCE", "t3z-integrations"),
		IntegrationJWTTTLSeconds: getEnvInt("T3Z_INTEGRATION_JWT_TTL_SECONDS", 300),
		FrontendOrigins:          frontendOrigins(getEnv("T3Z_FRONTEND_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")),
		StudioCookieSecure:       getEnv("T3Z_COOKIE_SECURE", "true") != "false",
		StudioCookieSameSite:     getEnv("T3Z_COOKIE_SAME_SITE", "lax"),
		StudioSessionHours:       getEnvInt("T3Z_SESSION_HOURS", 24),
		FirebaseProjectID:        getEnv("T3Z_FIREBASE_PROJECT_ID", os.Getenv("GOOGLE_CLOUD_PROJECT")),
		RazorpayKeyID:            os.Getenv("RAZORPAY_KEY_ID"),
		RazorpayKeySecret:        os.Getenv("RAZORPAY_KEY_SECRET"),
		RazorpayWebhookSecret:    os.Getenv("RAZORPAY_WEBHOOK_SECRET"),
		BillingWebhookSecret:     os.Getenv("BILLING_WEBHOOK_SECRET"),
		DemoWebhookURL:           os.Getenv("DEMO_WEBHOOK_URL"),
		DemoWebhookSecret:        os.Getenv("DEMO_WEBHOOK_SECRET"),
		ModuleOrigins:            frontendOrigins(os.Getenv("T3Z_MODULE_ORIGINS")),
		ModuleAPIToken:           os.Getenv("T3Z_MODULE_API_TOKEN"),
		TrustedProxies:           trustedProxies(os.Getenv("T3Z_TRUSTED_PROXY_CIDRS")),
	}
}

func frontendOrigins(value string) []string {
	origins := make([]string, 0)
	for _, entry := range strings.Split(value, ",") {
		origin := strings.TrimSpace(entry)
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.Contains(origin, "*") {
			continue
		}
		if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
			continue
		}
		origins = append(origins, parsed.Scheme+"://"+parsed.Host)
	}
	return origins
}

func (c *Config) AllowsFrontendOrigin(origin string) bool {
	for _, allowed := range c.FrontendOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func trustedProxies(value string) []*net.IPNet {
	proxies := make([]*net.IPNet, 0)
	for _, entry := range strings.Split(value, ",") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(entry))
		if err == nil {
			proxies = append(proxies, network)
		}
	}
	return proxies
}

func (c *Config) IsTrustedProxy(address net.IP) bool {
	for _, network := range c.TrustedProxies {
		if network.Contains(address) {
			return true
		}
	}
	return false
}
