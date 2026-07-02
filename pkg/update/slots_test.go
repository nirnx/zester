package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func testHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func newSM(t *testing.T) (*SlotManager, string) {
	t.Helper()
	base := t.TempDir() + "/binary"
	return NewSlotManager(base), base
}

func TestSlotManager_Stage(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("hello world")
	hash := testHash(data)

	if err := sm.Stage(data, hash); err != nil {
		t.Fatalf("Stage: %v", err)
	}

	staging := base + ".staging"
	got, err := os.ReadFile(staging)
	if err != nil {
		t.Fatalf("read staging: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("staging contents mismatch: got %q, want %q", got, data)
	}

	info, err := os.Stat(staging)
	if err != nil {
		t.Fatalf("stat staging: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("staging perm: got %o, want 0700", info.Mode().Perm())
	}
}

func TestSlotManager_Stage_HashMismatch(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("hello world")

	err := sm.Stage(data, "badhash")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("error should contain 'hash mismatch': %v", err)
	}

	staging := base + ".staging"
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatal("staging file should have been removed on hash mismatch")
	}
}

func TestSlotManager_Apply(t *testing.T) {
	sm, base := newSM(t)
	oldData := []byte("old binary")
	newData := []byte("new binary")

	if err := os.WriteFile(base, oldData, 0755); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := sm.Stage(newData, testHash(newData)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := sm.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if string(got) != string(newData) {
		t.Fatalf("current should be new binary: got %q", got)
	}

	prev, err := os.ReadFile(base + ".prev")
	if err != nil {
		t.Fatalf("read previous: %v", err)
	}
	if string(prev) != string(oldData) {
		t.Fatalf("previous should be old binary: got %q", prev)
	}

	if _, err := os.Stat(base + ".staging"); !os.IsNotExist(err) {
		t.Fatal("staging file should be gone after Apply")
	}
}

func TestSlotManager_Rollback(t *testing.T) {
	sm, base := newSM(t)
	oldData := []byte("old binary")
	newData := []byte("new binary")

	if err := os.WriteFile(base, oldData, 0755); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := sm.Stage(newData, testHash(newData)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := sm.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := sm.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current after rollback: %v", err)
	}
	if string(got) != string(oldData) {
		t.Fatalf("current should be old binary after rollback: got %q", got)
	}

	if _, err := os.Stat(base + ".prev"); !os.IsNotExist(err) {
		t.Fatal("previous file should be gone after Rollback")
	}
	if _, err := os.Stat(base + ".staging"); !os.IsNotExist(err) {
		t.Fatal("staging file should be gone after Rollback")
	}
}

func TestSlotManager_Confirm(t *testing.T) {
	sm, base := newSM(t)
	oldData := []byte("old binary")
	newData := []byte("new binary")

	if err := os.WriteFile(base, oldData, 0755); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := sm.Stage(newData, testHash(newData)); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if err := sm.Apply(); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if err := sm.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current after Confirm: %v", err)
	}
	if string(got) != string(newData) {
		t.Fatalf("current should still be new binary: got %q", got)
	}

	if _, err := os.Stat(base + ".staging"); !os.IsNotExist(err) {
		t.Fatal("staging should be gone after Confirm")
	}

	if _, err := os.Stat(base + ".prev"); os.IsNotExist(err) {
		t.Fatal("previous should still exist after Confirm (kept for future rollback)")
	}
}

func TestSlotManager_Rollback_NoPrevious(t *testing.T) {
	sm, base := newSM(t)

	if err := os.WriteFile(base, []byte("current"), 0755); err != nil {
		t.Fatalf("write current: %v", err)
	}

	err := sm.Rollback()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no previous binary") {
		t.Fatalf("error should contain 'no previous binary': %v", err)
	}
}

func TestSlotManager_Recover_StagingNoCurrent(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("staged binary")

	if err := os.WriteFile(base+".staging", data, 0755); err != nil {
		t.Fatalf("write staging: %v", err)
	}

	if err := sm.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current after Recover: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("current should have staging contents: got %q", got)
	}

	if _, err := os.Stat(base + ".staging"); !os.IsNotExist(err) {
		t.Fatal("staging should be gone after Recover")
	}
}

func TestSlotManager_Recover_PreviousNoCurrent(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("previous binary")

	if err := os.WriteFile(base+".prev", data, 0755); err != nil {
		t.Fatalf("write previous: %v", err)
	}

	if err := sm.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current after Recover: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("current should have previous contents: got %q", got)
	}

	if _, err := os.Stat(base + ".prev"); !os.IsNotExist(err) {
		t.Fatal("previous should be gone after Recover")
	}
}

func TestSlotManager_Recover_CleanState(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("current binary")

	if err := os.WriteFile(base, data, 0755); err != nil {
		t.Fatalf("write current: %v", err)
	}

	if err := sm.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := os.ReadFile(base)
	if err != nil {
		t.Fatalf("read current after Recover: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("current should be unchanged: got %q", got)
	}
}

func TestSlotManager_Verify(t *testing.T) {
	sm, base := newSM(t)
	data := []byte("verifiable content")
	hash := testHash(data)

	if err := os.WriteFile(base, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if err := sm.Verify(base, hash); err != nil {
		t.Fatalf("Verify with correct hash: %v", err)
	}

	err := sm.Verify(base, "wronghash")
	if err == nil {
		t.Fatal("expected error for wrong hash, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("error should contain 'hash mismatch': %v", err)
	}
}
