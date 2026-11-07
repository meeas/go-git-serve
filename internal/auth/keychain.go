package auth

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// keychainService is the service name tokens are stored under. The account
// is the app name, so each renamed copy of the binary has its own token.
const keychainService = "go-git-serve"

// ErrNoToken is returned when the keychain holds no token for the app.
var ErrNoToken = errors.New("no token stored in the OS keychain")

// SetToken stores tok in the OS keychain (Windows Credential Manager, macOS
// Keychain, or the Linux Secret Service).
func SetToken(app, tok string) error {
	return keyring.Set(keychainService, app, tok)
}

// GetToken reads the app's token from the OS keychain.
func GetToken(app string) (string, error) {
	v, err := keyring.Get(keychainService, app)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNoToken
	}
	return v, err
}

// DeleteToken removes the app's token from the OS keychain.
func DeleteToken(app string) error {
	err := keyring.Delete(keychainService, app)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNoToken
	}
	return err
}
