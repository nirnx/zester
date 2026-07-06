package enroll_test

import (
	"context"
	"testing"
	"time"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
	"github.com/nirnx/zester/pkg/enroll"
)

func testChallengeStoreSetup(t *testing.T) (*enroll.ChallengeStore, context.Context) {
	t.Helper()
	js := bustest.NewFakeJS()
	ctx := context.Background()
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	store, err := enroll.NewChallengeStore(ctx, js, nil)
	if err != nil {
		t.Fatalf("NewChallengeStore: %v", err)
	}

	return store, ctx
}

func TestChallengeStoreIssue(t *testing.T) {
	store, ctx := testChallengeStoreSetup(t)

	rec, err := store.Issue(ctx, "test-peel", "UABC123")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Verify challenge record fields.
	if rec.ChallengeID == "" {
		t.Error("ChallengeID is empty")
	}
	if len(rec.Challenge) != enroll.ChallengeSize {
		t.Errorf("Challenge length = %d, want %d", len(rec.Challenge), enroll.ChallengeSize)
	}
	if rec.PeelID != "test-peel" {
		t.Errorf("PeelID = %q, want %q", rec.PeelID, "test-peel")
	}
	if rec.PublicKey != "UABC123" {
		t.Errorf("PublicKey = %q, want %q", rec.PublicKey, "UABC123")
	}
	if rec.IssuedAt.IsZero() {
		t.Error("IssuedAt is zero")
	}
	if rec.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero")
	}
	if rec.Used {
		t.Error("Used should be false for new challenge")
	}

	// Verify expiry is set correctly (within tolerance).
	expectedExpiry := rec.IssuedAt.Add(enroll.ChallengeTTL)
	if !rec.ExpiresAt.Equal(expectedExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", rec.ExpiresAt, expectedExpiry)
	}
}

func TestChallengeStoreConsumeValid(t *testing.T) {
	store, ctx := testChallengeStoreSetup(t)

	// Issue a challenge.
	issued, err := store.Issue(ctx, "test-peel", "UABC123")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Consume it.
	consumed, err := store.Consume(ctx, issued.ChallengeID)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	// Verify fields match.
	if consumed.ChallengeID != issued.ChallengeID {
		t.Errorf("ChallengeID = %q, want %q", consumed.ChallengeID, issued.ChallengeID)
	}
	if string(consumed.Challenge) != string(issued.Challenge) {
		t.Error("Challenge bytes do not match")
	}
	if consumed.PeelID != issued.PeelID {
		t.Errorf("PeelID = %q, want %q", consumed.PeelID, issued.PeelID)
	}
	if consumed.PublicKey != issued.PublicKey {
		t.Errorf("PublicKey = %q, want %q", consumed.PublicKey, issued.PublicKey)
	}
}

func TestChallengeStoreConsumeDoubleConsume(t *testing.T) {
	store, ctx := testChallengeStoreSetup(t)

	// Issue a challenge.
	issued, err := store.Issue(ctx, "test-peel", "UABC123")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Consume it once.
	_, err = store.Consume(ctx, issued.ChallengeID)
	if err != nil {
		t.Fatalf("First Consume: %v", err)
	}

	// Attempt to consume again (should fail).
	_, err = store.Consume(ctx, issued.ChallengeID)
	if err == nil {
		t.Error("Second Consume should fail, got nil error")
	}
}

func TestChallengeStoreConsumeExpired(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	// Create challenge bucket with very short TTL for testing.
	shortTTL := 100 * time.Millisecond

	// Initialize storage first, then override the challenge bucket with short TTL.
	if err := bus.InitializeStorage(ctx, js); err != nil {
		t.Fatalf("initialize storage: %v", err)
	}

	// Re-create the challenge bucket with short TTL using CreateBucket.
	kv, err := bus.CreateBucket(ctx, js, bus.BucketConfig{
		Bucket: bus.BucketEnrollChallenges,
		TTL:    shortTTL,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	fakeKV := kv.(*bustest.FakeKV)

	challengeStore, err := enroll.NewChallengeStore(ctx, js, nil)
	if err != nil {
		t.Fatalf("NewChallengeStore: %v", err)
	}

	// Issue a challenge.
	issued, err := challengeStore.Issue(ctx, "test-peel", "UABC123")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Wait for expiry (FakeKV checks TTL on access).
	time.Sleep(shortTTL + 50*time.Millisecond)

	// Attempt to consume expired challenge.
	_, err = challengeStore.Consume(ctx, issued.ChallengeID)
	if err == nil {
		t.Error("Consume of expired challenge should fail, got nil error")
	}

	// Also verify that the key is gone from FakeKV.
	_, getErr := fakeKV.Get(ctx, issued.ChallengeID)
	if getErr == nil {
		t.Error("Expired challenge should be purged from KV")
	}
}

func TestChallengeStoreConsumeNotFound(t *testing.T) {
	store, ctx := testChallengeStoreSetup(t)

	_, err := store.Consume(ctx, "chl-nonexistent")
	if err == nil {
		t.Error("Consume with nonexistent challenge ID should fail, got nil error")
	}
}
