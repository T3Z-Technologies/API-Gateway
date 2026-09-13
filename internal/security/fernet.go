package security

import (
	"errors"
	"fmt"

	"github.com/fernet/fernet-go"
)

type FernetEncryptor struct {
	key *fernet.Key
}

func NewFernetEncryptor(b64Key string) (*FernetEncryptor, error) {
	if b64Key == "" {
		return nil, errors.New("missing Fernet encryption key")
	}

	key, err := fernet.DecodeKey(b64Key)
	if err != nil {
		return nil, fmt.Errorf("invalid Fernet key: %w", err)
	}

	return &FernetEncryptor{key: key}, nil
}

func (f *FernetEncryptor) Encrypt(value string) (string, error) {
	tok, err := fernet.EncryptAndSign([]byte(value), f.key)
	if err != nil {
		return "", err
	}
	return string(tok), nil
}

func (f *FernetEncryptor) Decrypt(token string) (string, error) {
	// Fernet tokens don't expire for decryption by default in our use-case (ttl = 0 ignores timestamp)
	msg := fernet.VerifyAndDecrypt([]byte(token), 0, []*fernet.Key{f.key})
	if msg == nil {
		return "", errors.New("failed to decrypt Fernet token")
	}
	return string(msg), nil
}
