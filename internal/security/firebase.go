package security

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"t3z/api-gateway/internal/config"
)

type FirebaseUser struct {
	UID    string                 `json:"user_id"`
	Email  string                 `json:"email"`
	Admin  bool                   `json:"admin"`
	Claims map[string]interface{} `json:"-"`
}

type FirebaseVerifier struct {
	cfg        *config.Config
	projectID  string
	certs      map[string]*rsa.PublicKey
	certsMux   sync.RWMutex
	certsExp   time.Time
	httpClient *http.Client
}

func NewFirebaseVerifier(cfg *config.Config) *FirebaseVerifier {
	projectID := cfg.FirebaseProjectID
	if cfg.FirebaseCredentials != "" {
		if data, err := os.ReadFile(cfg.FirebaseCredentials); err == nil {
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			if err := json.Unmarshal(data, &sa); err == nil && sa.ProjectID != "" {
				projectID = sa.ProjectID
			}
		}
	}

	return &FirebaseVerifier{
		cfg:        cfg,
		projectID:  projectID,
		certs:      make(map[string]*rsa.PublicKey),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *FirebaseVerifier) refreshCerts() error {
	v.certsMux.Lock()
	defer v.certsMux.Unlock()

	if time.Now().Before(v.certsExp) && len(v.certs) > 0 {
		return nil
	}

	resp, err := v.httpClient.Get("https://www.googleapis.com/robot/v1/metadata/x509/securetoken@system.gserviceaccount.com")
	if err != nil {
		return fmt.Errorf("failed to fetch google public certs: %w", err)
	}
	defer resp.Body.Close()

	var rawCerts map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&rawCerts); err != nil {
		return fmt.Errorf("failed to decode google public certs: %w", err)
	}

	parsedCerts := make(map[string]*rsa.PublicKey)
	for kid, certPEM := range rawCerts {
		block, _ := pem.Decode([]byte(certPEM))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			parsedCerts[kid] = pub
		}
	}

	if len(parsedCerts) == 0 {
		return errors.New("no valid google certificates parsed")
	}

	v.certs = parsedCerts
	// Cache for 6 hours by default
	v.certsExp = time.Now().Add(6 * time.Hour)
	return nil
}

func (v *FirebaseVerifier) VerifyIDToken(tokenString string) (*FirebaseUser, error) {
	if err := v.refreshCerts(); err != nil && len(v.certs) == 0 {
		return nil, err
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, ok := t.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing kid header in token")
		}

		v.certsMux.RLock()
		pubKey, found := v.certs[kid]
		v.certsMux.RUnlock()

		if !found {
			// Refresh certs if kid not found
			if err := v.refreshCerts(); err != nil {
				return nil, fmt.Errorf("unknown kid: %s", kid)
			}
			v.certsMux.RLock()
			pubKey, found = v.certs[kid]
			v.certsMux.RUnlock()
			if !found {
				return nil, fmt.Errorf("unknown kid: %s", kid)
			}
		}
		return pubKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("invalid firebase id token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid firebase token claims")
	}

	// Verify issuer & audience if projectID is known
	if v.projectID != "" {
		expectedIss := "https://securetoken.google.com/" + v.projectID
		if iss, ok := claims["iss"].(string); !ok || iss != expectedIss {
			return nil, fmt.Errorf("token issuer mismatch: got %s, expected %s", iss, expectedIss)
		}
		if aud, ok := claims["aud"].(string); !ok || aud != v.projectID {
			return nil, fmt.Errorf("token audience mismatch: got %s, expected %s", aud, v.projectID)
		}
	}

	uid, _ := claims["user_id"].(string)
	if uid == "" {
		uid, _ = claims["sub"].(string)
	}
	email, _ := claims["email"].(string)

	admin := false
	if adminClaim, ok := claims["admin"]; ok {
		switch a := adminClaim.(type) {
		case bool:
			admin = a
		case string:
			admin = strings.ToLower(a) == "true"
		}
	}

	return &FirebaseUser{
		UID:    uid,
		Email:  email,
		Admin:  admin,
		Claims: claims,
	}, nil
}
