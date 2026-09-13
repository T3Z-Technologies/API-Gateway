package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Argon2Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var DefaultArgon2Params = &Argon2Params{
	Memory:      65536,
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

var (
	ErrInvalidHash         = errors.New("the encoded hash is not in the correct format")
	ErrIncompatibleVersion = errors.New("incompatible version of argon2")
)

// HashSecret generates a standard Argon2id PHC string compatible with Python argon2-cffi.
func HashSecret(secret string) (string, error) {
	salt := make([]byte, DefaultArgon2Params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := argon2.IDKey(
		[]byte(secret),
		salt,
		DefaultArgon2Params.Iterations,
		DefaultArgon2Params.Memory,
		DefaultArgon2Params.Parallelism,
		DefaultArgon2Params.KeyLength,
	)

	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		DefaultArgon2Params.Memory,
		DefaultArgon2Params.Iterations,
		DefaultArgon2Params.Parallelism,
		b64Salt,
		b64Hash,
	)

	return encoded, nil
}

// VerifySecret checks if a secret matches the encoded Argon2id hash.
func VerifySecret(encodedHash, secret string) bool {
	vals := strings.Split(encodedHash, "$")
	if len(vals) != 6 {
		return false
	}

	if vals[1] != "argon2id" && vals[1] != "argon2i" {
		return false
	}

	var version int
	_, err := fmt.Sscanf(vals[2], "v=%d", &version)
	if err != nil || version != argon2.Version {
		return false
	}

	var memory, iterations uint32
	var parallelism uint64
	for _, part := range strings.Split(vals[3], ",") {
		kv := strings.Split(part, "=")
		if len(kv) != 2 {
			return false
		}
		switch kv[0] {
		case "m":
			m, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false
			}
			memory = uint32(m)
		case "t":
			t, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false
			}
			iterations = uint32(t)
		case "p":
			p, err := strconv.ParseUint(kv[1], 10, 8)
			if err != nil {
				return false
			}
			parallelism = p
		}
	}

	salt, err := decodeBase64Flexible(vals[4])
	if err != nil {
		return false
	}

	expectedHash, err := decodeBase64Flexible(vals[5])
	if err != nil {
		return false
	}

	var comparisonHash []byte
	if vals[1] == "argon2id" {
		comparisonHash = argon2.IDKey(
			[]byte(secret),
			salt,
			iterations,
			memory,
			uint8(parallelism),
			uint32(len(expectedHash)),
		)
	} else {
		comparisonHash = argon2.Key(
			[]byte(secret),
			salt,
			iterations,
			memory,
			uint8(parallelism),
			uint32(len(expectedHash)),
		)
	}

	return subtle.ConstantTimeCompare(expectedHash, comparisonHash) == 1
}

func decodeBase64Flexible(s string) ([]byte, error) {
	// Try RawStdEncoding (no padding) first
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	// Fall back to StdEncoding
	return base64.StdEncoding.DecodeString(s)
}
