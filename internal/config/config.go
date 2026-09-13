package config

import (
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
	}
}
