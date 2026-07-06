package enroll

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nirnx/zester/pkg/auth"
)

// SaveCredentials writes the JWT and peel's seed to a .creds file.
// The peel's seed was generated locally -- the JWT is signed by the
// master's account key. Together they form a valid NATS creds file.
func SaveCredentials(credsDir, peelID, userJWT string, seed []byte) (string, error) {
	if err := os.MkdirAll(credsDir, 0700); err != nil {
		return "", fmt.Errorf("enroll: create creds directory: %w", err)
	}

	credsPath := filepath.Join(credsDir, peelID+".creds")

	if err := auth.WriteCredsFile(credsPath, userJWT, seed); err != nil {
		return "", fmt.Errorf("enroll: write creds file: %w", err)
	}

	// Verify permissions.
	info, err := os.Stat(credsPath)
	if err == nil && info.Mode().Perm() != 0600 {
		if chErr := os.Chmod(credsPath, 0600); chErr != nil {
			return "", fmt.Errorf("enroll: fix creds permissions: %w", chErr)
		}
	}

	return credsPath, nil
}

// SaveSeed persists the peel's nkey seed for re-enrollment attempts.
func SaveSeed(authDir, peelID string, seed []byte) (string, error) {
	if err := os.MkdirAll(authDir, 0700); err != nil {
		return "", fmt.Errorf("enroll: create auth directory: %w", err)
	}

	seedPath := filepath.Join(authDir, peelID+".seed")
	if err := os.WriteFile(seedPath, seed, 0600); err != nil {
		return "", fmt.Errorf("enroll: write seed file: %w", err)
	}

	return seedPath, nil
}

// LoadOrGenerateKey loads an existing nkey seed from disk, or generates
// a new user nkey and saves the seed. Returns the KeyBundle.
func LoadOrGenerateKey(authDir, peelID string) (*auth.KeyBundle, error) {
	seedPath := filepath.Join(authDir, peelID+".seed")

	if _, err := os.Stat(seedPath); err == nil {
		kb, err := auth.LoadKeyBundleFromFile(auth.RoleUser, seedPath)
		if err != nil {
			return nil, fmt.Errorf("enroll: load existing seed: %w", err)
		}
		return kb, nil
	}

	kb, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		return nil, fmt.Errorf("enroll: generate key: %w", err)
	}

	if _, err := SaveSeed(authDir, peelID, kb.Seed); err != nil {
		return nil, err
	}

	return kb, nil
}

// HasCredentials checks if a .creds file already exists for the peel.
func HasCredentials(authDir, peelID string) bool {
	credsPath := filepath.Join(authDir, peelID+".creds")
	_, err := os.Stat(credsPath)
	return err == nil
}
