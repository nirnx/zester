package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// SlotManager manages three filesystem slots for atomic binary updates:
// current: basePath — the running binary
// previous: basePath.prev — last known good (rollback target)
// staging: basePath.staging — downloaded, verified, pending swap
type SlotManager struct {
	basePath string
}

func NewSlotManager(basePath string) *SlotManager {
	return &SlotManager{basePath: basePath}
}

func (s *SlotManager) currentPath() string  { return s.basePath }
func (s *SlotManager) previousPath() string { return s.basePath + ".prev" }
func (s *SlotManager) stagingPath() string  { return s.basePath + ".staging" }

// Stage writes data to the staging slot and verifies its hash.
func (s *SlotManager) Stage(data []byte, expectedHash string) error {
	if err := os.WriteFile(s.stagingPath(), data, 0700); err != nil {
		return fmt.Errorf("update: stage: write: %w", err)
	}
	got, err := s.hashFile(s.stagingPath())
	if err != nil {
		_ = os.Remove(s.stagingPath())
		return fmt.Errorf("update: stage: hash: %w", err)
	}
	if got != expectedHash {
		_ = os.Remove(s.stagingPath())
		return fmt.Errorf("update: stage: hash mismatch: got %s, want %s", got, expectedHash)
	}
	return nil
}

// Apply performs an atomic two-rename swap: current → previous, staging → current.
func (s *SlotManager) Apply() error {
	if _, err := os.Stat(s.previousPath()); err == nil {
		if err := os.Remove(s.previousPath()); err != nil {
			return fmt.Errorf("update: apply: remove previous: %w", err)
		}
	}
	if err := os.Rename(s.currentPath(), s.previousPath()); err != nil {
		return fmt.Errorf("update: apply: current to previous: %w", err)
	}
	if err := os.Rename(s.stagingPath(), s.currentPath()); err != nil {
		// Attempt rollback: restore previous → current
		_ = os.Rename(s.previousPath(), s.currentPath())
		return fmt.Errorf("update: apply: staging to current: %w", err)
	}
	return nil
}

// Rollback restores the previous binary as current.
func (s *SlotManager) Rollback() error {
	if _, err := os.Stat(s.previousPath()); os.IsNotExist(err) {
		return fmt.Errorf("update: rollback: no previous binary")
	}
	if _, err := os.Stat(s.currentPath()); err == nil {
		if err := os.Remove(s.currentPath()); err != nil {
			return fmt.Errorf("update: rollback: remove current: %w", err)
		}
	}
	if err := os.Rename(s.previousPath(), s.currentPath()); err != nil {
		return fmt.Errorf("update: rollback: previous to current: %w", err)
	}
	_ = os.Remove(s.stagingPath())
	return nil
}

// Confirm cleans up staging after a successful update is confirmed.
func (s *SlotManager) Confirm() error {
	_ = os.Remove(s.stagingPath())
	return nil
}

// Verify checks that the file at path matches expectedHash.
func (s *SlotManager) Verify(path string, expectedHash string) error {
	got, err := s.hashFile(path)
	if err != nil {
		return fmt.Errorf("update: verify: %w", err)
	}
	if got != expectedHash {
		return fmt.Errorf("update: verify: hash mismatch: got %s, want %s", got, expectedHash)
	}
	return nil
}

// Recover detects and resolves partial swaps on startup.
func (s *SlotManager) Recover() error {
	_, stagingErr := os.Stat(s.stagingPath())
	_, currentErr := os.Stat(s.currentPath())
	_, previousErr := os.Stat(s.previousPath())

	stagingExists := stagingErr == nil
	currentExists := currentErr == nil
	previousExists := previousErr == nil

	if stagingExists && !currentExists {
		// Mid-swap: staging was written but rename to current didn't complete
		if err := os.Rename(s.stagingPath(), s.currentPath()); err != nil {
			return fmt.Errorf("update: recover: staging to current: %w", err)
		}
		return nil
	}
	if previousExists && !currentExists {
		// Crashed during apply after current was moved to previous
		if err := os.Rename(s.previousPath(), s.currentPath()); err != nil {
			return fmt.Errorf("update: recover: previous to current: %w", err)
		}
		return nil
	}
	return nil
}

// hashFile computes the SHA-256 hex digest of the file at path.
func (s *SlotManager) hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
