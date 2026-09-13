package security

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
)

var ErrUnauthorizedBasic = errors.New("Unauthorized")

func GetBasicCredentials(r *http.Request) (string, string, error) {
	authHeader := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Basic ") {
		return "", "", ErrUnauthorizedBasic
	}

	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authHeader[6:]))
	if err != nil {
		return "", "", ErrUnauthorizedBasic
	}

	parts := strings.SplitN(string(payload), ":", 2)
	if len(parts) != 2 {
		return "", "", ErrUnauthorizedBasic
	}

	return parts[0], parts[1], nil
}
