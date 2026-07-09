package settings

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/bus/bustest"
)

// TestSecretPlaceholder_Substitution verifies that SanitizeFile replaces
// !encrypted values with __ZESTER_SECRET:key__ placeholders and that
// the extracted secrets contain the original plaintext values.
func TestSecretPlaceholder_Substitution(t *testing.T) {
	input := []byte(`
db_host: localhost
db_password: !encrypted "s3cret-prod-password"
api_key: !encrypted "key-abc-123"
listen_port: 8080
`)

	sanitized, err := SanitizeFile(input)
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}

	content := string(sanitized.Content)

	// Verify placeholders are present in sanitized content.
	if !strings.Contains(content, SecretPlaceholderPrefix+"db_password"+SecretPlaceholderSuffix) {
		t.Errorf("sanitized content missing db_password placeholder:\n%s", content)
	}
	if !strings.Contains(content, SecretPlaceholderPrefix+"api_key"+SecretPlaceholderSuffix) {
		t.Errorf("sanitized content missing api_key placeholder:\n%s", content)
	}

	// Verify plaintext secrets are NOT in sanitized content.
	if strings.Contains(content, "s3cret-prod-password") {
		t.Errorf("sanitized content still contains plaintext secret 's3cret-prod-password'")
	}
	if strings.Contains(content, "key-abc-123") {
		t.Errorf("sanitized content still contains plaintext secret 'key-abc-123'")
	}

	// Verify non-secret values are preserved.
	if !strings.Contains(content, "localhost") {
		t.Errorf("sanitized content should still contain 'localhost'")
	}

	// Verify extracted secrets map.
	if len(sanitized.Secrets) != 2 {
		t.Fatalf("secrets count = %d, want 2", len(sanitized.Secrets))
	}
	if sanitized.Secrets["db_password"] != "s3cret-prod-password" {
		t.Errorf("secrets[db_password] = %q, want s3cret-prod-password", sanitized.Secrets["db_password"])
	}
	if sanitized.Secrets["api_key"] != "key-abc-123" {
		t.Errorf("secrets[api_key] = %q, want key-abc-123", sanitized.Secrets["api_key"])
	}

	// Verify IsSecretPlaceholder and ExtractSecretKey work correctly.
	placeholder := SecretPlaceholderPrefix + "db_password" + SecretPlaceholderSuffix
	if !IsSecretPlaceholder(placeholder) {
		t.Errorf("IsSecretPlaceholder(%q) = false, want true", placeholder)
	}
	if key := ExtractSecretKey(placeholder); key != "db_password" {
		t.Errorf("ExtractSecretKey(%q) = %q, want db_password", placeholder, key)
	}
}

// TestRawFilesSanitized verifies that .zy files stored in the shared
// settings-files KV bucket do NOT contain plaintext !encrypted values.
// Per HA-R1, raw .zy files must have secrets replaced with placeholders
// before being stored in shared KV.
func TestRawFilesSanitized(t *testing.T) {
	js := bustest.NewFakeJS()

	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: "settings-files-sanitize"})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: "secrets-sanitize"})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	masterKB, _ := auth.GenerateKeyBundle(auth.RoleOperator)
	masterEnc, _ := auth.NewEncryptor(masterKB)

	pub, err := NewPublisher(PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	// Simulate raw files with !encrypted values.
	files := map[string][]byte{
		"creds.zy": []byte(`
db_user: appuser
db_password: !encrypted "supersecretpassword"
api_token: !encrypted "tok-999"
app_name: zester
`),
		"base.zy": []byte(`
log_level: info
region: us-east-1
`),
	}

	secrets, _, err := pub.PublishRawFiles(ctx, files)
	if err != nil {
		t.Fatalf("publish raw files: %v", err)
	}

	// Verify extracted secrets contain the plaintext (now per-file).
	flat := secrets.Flat()
	if flat["db_password"] != "supersecretpassword" {
		t.Errorf("secrets[db_password] = %q, want supersecretpassword", flat["db_password"])
	}
	if flat["api_token"] != "tok-999" {
		t.Errorf("secrets[api_token] = %q, want tok-999", flat["api_token"])
	}

	// Read back the stored file from KV and verify NO plaintext secrets.
	entry, err := filesKV.Get(ctx, "creds.zy")
	if err != nil {
		t.Fatalf("get creds.zy from KV: %v", err)
	}
	stored := string(entry.Value())

	if strings.Contains(stored, "supersecretpassword") {
		t.Errorf("stored creds.zy contains plaintext secret 'supersecretpassword':\n%s", stored)
	}
	if strings.Contains(stored, "tok-999") {
		t.Errorf("stored creds.zy contains plaintext secret 'tok-999':\n%s", stored)
	}

	// Verify placeholders ARE present.
	if !strings.Contains(stored, SecretPlaceholderPrefix+"db_password"+SecretPlaceholderSuffix) {
		t.Errorf("stored creds.zy missing db_password placeholder:\n%s", stored)
	}

	// Non-secret values should still be present.
	if !strings.Contains(stored, "appuser") {
		t.Errorf("stored creds.zy should still contain 'appuser'")
	}

	// base.zy should be stored as-is (no secrets).
	baseEntry, err := filesKV.Get(ctx, "base.zy")
	if err != nil {
		t.Fatalf("get base.zy from KV: %v", err)
	}
	baseStored := string(baseEntry.Value())
	if !strings.Contains(baseStored, "us-east-1") {
		t.Errorf("stored base.zy should contain 'us-east-1'")
	}
}

// TestResolveForPeel_TwoPhaseRender tests the two-phase rendering approach:
// Phase 1: render templates with placeholders (secrets are __ZESTER_SECRET:key__)
// Phase 2: substitute decrypted secret values into the rendered result.
// This tests the full Resolver pipeline from sanitized shared KV files.
func TestResolveForPeel_TwoPhaseRender(t *testing.T) {
	js := bustest.NewFakeJS()

	ctx := context.Background()

	// Create buckets for the peel-side resolver.
	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	masterKB, _ := auth.GenerateKeyBundle(auth.RoleOperator)
	masterEnc, _ := auth.NewEncryptor(masterKB)
	peelKB, _ := auth.GenerateKeyBundle(auth.RoleUser)
	peelCurvePub, _ := peelKB.CurvePublicKey()
	peelEnc, _ := auth.NewEncryptor(peelKB)

	// Phase 1: Master sanitizes and publishes raw files.
	pub, err := NewPublisher(PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	rawFiles := map[string][]byte{
		"top.zy": []byte(`
base:
  '*':
    - app
`),
		"app.zy": []byte(`
app_name: myapp
db_host: "{{ facts.db_host }}"
db_password: !encrypted "prod-db-pass"
`),
	}

	secrets, _, err := pub.PublishRawFiles(ctx, rawFiles)
	if err != nil {
		t.Fatalf("publish raw files: %v", err)
	}

	// Master encrypts secrets for this peel (filtered by matching refs).
	if err := pub.PublishSecrets(ctx, "peel-01", secrets.Flat(), peelCurvePub); err != nil {
		t.Fatalf("publish secrets: %v", err)
	}

	// Phase 2: Peel resolves settings locally.
	dir := t.TempDir()
	eng := newTestEngine(t, dir)

	resolver, err := NewResolver(ResolverConfig{
		PeelID:    "peel-01",
		JS:        js,
		Engine:    eng,
		Encryptor: peelEnc,
		SenderPub: masterEnc.PublicKey(),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	facts := map[string]any{"db_host": "db.prod.internal"}
	result, err := resolver.Resolve(ctx, facts)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Template variables should be rendered.
	if result["app_name"] != "myapp" {
		t.Errorf("app_name = %v, want myapp", result["app_name"])
	}
	if result["db_host"] != "db.prod.internal" {
		t.Errorf("db_host = %v, want db.prod.internal", result["db_host"])
	}

	// Secret should be decrypted (mergeSecrets substitutes the plaintext).
	dbPass, ok := result["db_password"].(string)
	if !ok {
		t.Fatalf("db_password not string: %T", result["db_password"])
	}
	if dbPass != "prod-db-pass" {
		t.Errorf("db_password = %q, want prod-db-pass", dbPass)
	}

	_ = peelEnc
}

// TestResolveForPeel_CrossFileSecretReference verifies that when a settings file
// references an encrypted value from a previously loaded file via {{ settings.X }},
// the template sees the decrypted value (not the __ZESTER_SECRET: placeholder).
func TestResolveForPeel_CrossFileSecretReference(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	masterKB, _ := auth.GenerateKeyBundle(auth.RoleOperator)
	masterEnc, _ := auth.NewEncryptor(masterKB)
	peelKB, _ := auth.GenerateKeyBundle(auth.RoleUser)
	peelCurvePub, _ := peelKB.CurvePublicKey()
	peelEnc, _ := auth.NewEncryptor(peelKB)

	pub, err := NewPublisher(PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	// top.zy targets '*' -> [creds, monitoring] — order matters (creds first).
	rawFiles := map[string][]byte{
		"top.zy": []byte(`
base:
  '*':
    - creds
    - monitoring
`),
		"creds.zy": []byte(`
db_password: !encrypted "prod-pass"
`),
		"monitoring.zy": []byte(`
connection_string: "postgres://user:{{ settings.db_password }}@host/db"
`),
	}

	secrets, _, err := pub.PublishRawFiles(ctx, rawFiles)
	if err != nil {
		t.Fatalf("publish raw files: %v", err)
	}

	if err := pub.PublishSecrets(ctx, "peel-01", secrets.Flat(), peelCurvePub); err != nil {
		t.Fatalf("publish secrets: %v", err)
	}

	dir := t.TempDir()
	eng := newTestEngine(t, dir)

	resolver, err := NewResolver(ResolverConfig{
		PeelID:    "peel-01",
		JS:        js,
		Engine:    eng,
		Encryptor: peelEnc,
		SenderPub: masterEnc.PublicKey(),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Secret should be decrypted.
	dbPass, ok := result["db_password"].(string)
	if !ok {
		t.Fatalf("db_password not string: %T", result["db_password"])
	}
	if dbPass != "prod-pass" {
		t.Errorf("db_password = %q, want prod-pass", dbPass)
	}

	// Cross-file reference should resolve to the decrypted value, not the placeholder.
	connStr, ok := result["connection_string"].(string)
	if !ok {
		t.Fatalf("connection_string not string: %T", result["connection_string"])
	}
	if connStr != "postgres://user:prod-pass@host/db" {
		t.Errorf("connection_string = %q, want postgres://user:prod-pass@host/db", connStr)
	}
}

// TestResolveForPeel_MergeOrder verifies that when multiple settings files
// match a peel via top.zy, later files override earlier ones in the merge order.
func TestResolveForPeel_MergeOrder(t *testing.T) {
	js := bustest.NewFakeJS()
	ctx := context.Background()

	filesKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSettingsFiles})
	if err != nil {
		t.Fatalf("create files bucket: %v", err)
	}
	secretsKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{Bucket: bus.BucketSecrets})
	if err != nil {
		t.Fatalf("create secrets bucket: %v", err)
	}

	masterKB, _ := auth.GenerateKeyBundle(auth.RoleOperator)
	masterEnc, _ := auth.NewEncryptor(masterKB)
	peelKB, _ := auth.GenerateKeyBundle(auth.RoleUser)
	peelEnc, _ := auth.NewEncryptor(peelKB)

	pub, err := NewPublisher(PublisherConfig{
		SettingsDir:     t.TempDir(),
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}

	rawFiles := map[string][]byte{
		"top.zy": []byte(`
base:
  '*':
    - base
    - override
`),
		"base.zy": []byte(`
log_level: debug
workers: 2
app: myapp
`),
		"override.zy": []byte(`
log_level: warn
workers: 16
`),
	}

	if _, _, err := pub.PublishRawFiles(ctx, rawFiles); err != nil {
		t.Fatalf("publish raw files: %v", err)
	}

	dir := t.TempDir()
	eng := newTestEngine(t, dir)

	resolver, err := NewResolver(ResolverConfig{
		PeelID:    "node-01",
		JS:        js,
		Engine:    eng,
		Encryptor: peelEnc,
		SenderPub: masterEnc.PublicKey(),
	})
	if err != nil {
		t.Fatalf("create resolver: %v", err)
	}

	result, err := resolver.Resolve(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Override should win for overlapping keys.
	if result["log_level"] != "warn" {
		t.Errorf("log_level = %v, want warn (override)", result["log_level"])
	}
	if result["workers"] != 16 {
		t.Errorf("workers = %v, want 16 (override)", result["workers"])
	}
	// Non-overlapping key from base should survive.
	if result["app"] != "myapp" {
		t.Errorf("app = %v, want myapp (base)", result["app"])
	}
}

// TestCompiledSettingsPublishBack verifies that after a peel resolves its
// settings, it can publish the compiled result back to a settings-compiled
// KV bucket for observability (per HA-R10). This preserves the ability to
// inspect a peel's settings centrally.
func TestCompiledSettingsPublishBack(t *testing.T) {
	js := bustest.NewFakeJS()

	ctx := context.Background()

	// Create the settings-compiled bucket for publish-back.
	compiledKV, err := bus.CreateBucket(ctx, js, bus.BucketConfig{
		Bucket: "settings-compiled",
	})
	if err != nil {
		t.Fatalf("create compiled bucket: %v", err)
	}

	// Simulate compiled settings from a peel's resolver.
	peelID := "web-42"
	compiledSettings := map[string]any{
		"app_name":  "zester",
		"log_level": "info",
		"listen":    80,
		"db_host":   "db.prod.internal",
		"region":    "us-east-1",
	}

	// Peel publishes compiled settings back to KV.
	if _, err := bus.KVPut(ctx, compiledKV, peelID, compiledSettings); err != nil {
		t.Fatalf("publish compiled settings: %v", err)
	}

	// An operator (or another master) can read back the compiled settings.
	var readback map[string]any
	if err := bus.KVGet(ctx, compiledKV, peelID, &readback); err != nil {
		t.Fatalf("read compiled settings: %v", err)
	}

	if readback["app_name"] != "zester" {
		t.Errorf("app_name = %v, want zester", readback["app_name"])
	}
	if readback["log_level"] != "info" {
		t.Errorf("log_level = %v, want info", readback["log_level"])
	}
	if readback["db_host"] != "db.prod.internal" {
		t.Errorf("db_host = %v, want db.prod.internal", readback["db_host"])
	}
	if readback["region"] != "us-east-1" {
		t.Errorf("region = %v, want us-east-1", readback["region"])
	}
	if fmt.Sprintf("%v", readback["listen"]) != "80" {
		t.Errorf("listen = %v, want 80", readback["listen"])
	}
}
