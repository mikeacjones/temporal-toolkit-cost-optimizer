// Package secrets keeps tcopt's credentials in the OS keychain: the macOS
// Keychain, the Secret Service on Linux, or the Windows Credential Manager.
// Secrets never go into config files, Workflow inputs, or history.
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "tcopt"

// ErrNotFound means the keychain has no entry for the key.
var ErrNotFound = errors.New("not in the keychain")

// Get reads a secret.
func Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

// Set writes a secret, replacing any existing value.
func Set(key, value string) error {
	return keyring.Set(service, key, value)
}

// Delete removes a secret. Deleting a missing secret isn't an error.
func Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// MockForTests replaces the keychain with an in-memory store.
func MockForTests() { keyring.MockInit() }
