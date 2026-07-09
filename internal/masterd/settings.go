package masterd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/fileserver"
	"github.com/nirnx/zester/pkg/settings"
	"github.com/nirnx/zester/pkg/statefiles"
)

// startSettingsPublisher loads the account encryption key, creates the
// peel-side rendering publisher, publishes the master curve public key
// (every master — peels need it regardless of which master publishes the
// files), and LOADS the raw settings files: per-file secrets are extracted
// into d.allSecrets and top.zy is parsed into d.topFile. Loading runs on
// EVERY master because the facts watcher callback (handleFactsUpdate)
// re-encrypts secrets from these fields; the KV writes of the sanitized
// files themselves (PublishRawFiles) are performed only by the
// publisher-lease holder — see startPublisherLease/publishSettingsFiles.
//
// It populates d.accountKP (reused by the credential issuer in
// startEnrollment) plus d.publisher, d.rawSettingsFiles, d.allSecrets, and
// d.topFile (read later by handleFactsUpdate on the facts watcher goroutine
// — all assigned here, before the watcher starts).
func (d *Daemon) startSettingsPublisher(ctx context.Context) error {
	// Load account key for settings encryption.
	accountKP, err := auth.LoadKeyBundleFromFile(auth.RoleAccount, d.cfg.AuthDir+"/account.seed")
	if err != nil {
		return fmt.Errorf("load account key: %w", err)
	}
	d.accountKP = accountKP

	masterEnc, err := auth.NewEncryptor(accountKP)
	if err != nil {
		return fmt.Errorf("create master encryptor: %w", err)
	}
	d.masterEnc = masterEnc
	d.logger.Info("encryption keys loaded")

	// Initialize peel-side rendering publisher.
	filesKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketSettingsFiles)
	if err != nil {
		return fmt.Errorf("get settings-files bucket: %w", err)
	}
	secretsKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketSecrets)
	if err != nil {
		return fmt.Errorf("get secrets bucket: %w", err)
	}

	publisher, err := settings.NewPublisher(settings.PublisherConfig{
		SettingsDir:     d.cfg.SettingsDir,
		FilesKV:         filesKV,
		SecretsKV:       secretsKV,
		MasterEncryptor: masterEnc,
	})
	if err != nil {
		return fmt.Errorf("create settings publisher: %w", err)
	}
	d.publisher = publisher

	// Sealed master-settings replica: the RAW settings tree (plaintext
	// !encrypted values included), each file sealed to the shared account
	// curve key — any master opens it, no peel can, and JetStream at-rest
	// sees only ciphertext. This is how standby settings dirs converge
	// (files_mirror); the sanitized settings-files bucket stays the
	// peel-facing distribution. Manifest hashes cover the plaintext, so the
	// hash-gate holds despite randomized seals.
	masterSettingsKV, err := bus.GetBucket(ctx, d.client.JetStream(), bus.BucketMasterSettings)
	if err != nil {
		return fmt.Errorf("get master-settings bucket: %w", err)
	}
	d.masterSettingsPublisher = statefiles.NewPublisher(statefiles.PublisherConfig{
		StatesDir: d.cfg.SettingsDir,
		KV:        masterSettingsKV,
		Logger:    d.logger,
		EncodeValue: func(_ string, plaintext []byte) ([]byte, error) {
			return masterEnc.Seal(plaintext, masterEnc.PublicKey())
		},
		// KEYED manifest hash: an unkeyed SHA-256 of a secret-bearing raw
		// file would be an offline brute-force oracle for any $KV.> reader
		// (admin creds, backups). HMAC under the account seed verifies only
		// for account-key holders (all masters) and stays deterministic, so
		// the hash-gate still holds.
		HashValue: d.masterSettingsHash,
	})

	// Publish master's curve public key so peels can decrypt secrets.
	// Deliberately NOT lease-gated: every master must be able to encrypt
	// secrets, and the key is identical across masters sharing account.seed.
	masterCurvePub := masterEnc.PublicKey()
	if _, err := secretsKV.Put(ctx, settings.MasterCurvePubKey, []byte(masterCurvePub)); err != nil {
		d.logger.Warn("failed to publish master curve public key", "error", err)
	} else {
		d.logger.Info("published master curve public key for peel decryption")
	}

	// Publish the discovery/refresh bootstrap document (CA bundle + fleet
	// NATS endpoints) to the cluster-info key, same non-lease-gated
	// idempotent pattern. No-op unless embedded CA or nats_advertise_urls
	// is configured.
	d.publishClusterInfo(ctx, secretsKV)

	// Load raw settings files and extract secrets for peel-side rendering.
	rawSettingsFiles, err := loadSettingsFiles(d.cfg.SettingsDir)
	if err != nil {
		d.logger.Warn("failed to load settings files for peel-side rendering", "error", err)
	}
	d.rawSettingsFiles = rawSettingsFiles
	if len(rawSettingsFiles) > 0 {
		d.allSecrets = extractFileSecrets(rawSettingsFiles, d.logger)
		d.logger.Info("settings files loaded for peel-side rendering",
			"files", len(rawSettingsFiles),
			"secrets", len(d.allSecrets),
		)
		// Parse top.zy for per-peel secret filtering.
		if topData, ok := rawSettingsFiles["top.zy"]; ok {
			d.topFile, err = settings.ParseTopFile(topData)
			if err != nil {
				d.logger.Warn("failed to parse top.zy for secret filtering", "error", err)
			}
		}
	}
	return nil
}

// publishSettingsFiles re-reads the on-disk settings tree, sanitizes it, and
// writes it to the shared settings-files bucket. Called only by the
// publisher-lease holder (initial publish, republish ticker, file watcher,
// fileserver-update service). Hash-gated unless force: an unchanged tree
// writes nothing and no peel resyncs.
//
// Re-reading (rather than reusing the boot-time d.rawSettingsFiles) is what
// makes on-disk edits publishable without a master restart. When the tree
// changed, the refreshed raw files, extracted secrets, and reparsed top.zy
// are stored back (under settingsMu — handleFactsUpdate reads them from the
// facts watcher goroutine) so per-peel secret encryption follows the live
// tree too.
func (d *Daemon) publishSettingsFiles(ctx context.Context, force bool) fileserver.SetResult {
	if d.cfg == nil || d.publisher == nil {
		return fileserver.SetResult{Name: "settings"}
	}
	files, err := loadSettingsFiles(d.cfg.SettingsDir)
	if err != nil {
		d.logger.Warn("failed to load settings files", "dir", d.cfg.SettingsDir, "error", err)
		return fileserver.SetResult{Name: "settings", Err: err.Error()}
	}
	if len(files) == 0 {
		// AllowEmpty-style caution: never wipe the settings bucket from an
		// empty or missing local dir; the boot-time behavior (skip when
		// nothing loaded) is preserved.
		return fileserver.SetResult{Name: "settings"}
	}

	// Sealed masters-only replica FIRST, from the SAME file set: on an
	// interruption between here and the sanitized publish the standby
	// mirrors the newer sealed tree, so a takeover rolls forward instead of
	// reverting settings.
	d.publishSealedSettings(ctx, files, force)

	publish := d.publisher.PublishRawFiles
	if force {
		publish = d.publisher.PublishRawFilesForce
	}
	secrets, changed, err := publish(ctx, files)
	if err != nil {
		d.logger.Warn("failed to publish raw settings files", "error", err)
		return fileserver.SetResult{Name: "settings", Err: err.Error()}
	}

	// Refresh the secret-encryption inputs to match the published tree.
	d.refreshSettingsState(ctx, files, secrets)

	if !changed {
		d.logger.Debug("settings files unchanged, publish skipped", "files", len(files))
		return fileserver.SetResult{Name: "settings", Files: len(files), Changed: false}
	}
	d.logger.Info("published raw settings files for peel-side rendering",
		"files", len(files),
		"secrets", len(secrets),
	)
	return fileserver.SetResult{Name: "settings", Files: len(files), Changed: true}
}

// masterSettingsHash is the account-seed-keyed HMAC-SHA256 used for the
// sealed replica's manifest (see startSettingsPublisher). Keyed so the
// plaintext-derived manifest is not an offline oracle; deterministic so the
// hash-gate holds.
func (d *Daemon) masterSettingsHash(plaintext []byte) string {
	mac := hmac.New(sha256.New, d.accountKP.Seed)
	mac.Write(plaintext)
	return hex.EncodeToString(mac.Sum(nil))
}

// publishSealedSettings replicates the RAW settings tree (the given file
// set) to the sealed masters-only bucket. It is published FROM THE SAME
// files map as, and BEFORE, the peel-facing sanitized publish: an
// interruption then leaves the sealed replica newer-or-equal, so a takeover
// rolls forward (mirrors the newer tree, completes the sanitized publish)
// instead of reverting settings. Log-only — a master-to-master concern, not
// part of the fileserver-update reply. Skipped when files_mirror is off (no
// standby consumes it) or before startSettingsPublisher ran.
func (d *Daemon) publishSealedSettings(ctx context.Context, files map[string][]byte, force bool) {
	if d.masterSettingsPublisher == nil || d.cfg == nil || !d.cfg.FilesMirror {
		return
	}
	publish := d.masterSettingsPublisher.PublishFiles
	if force {
		publish = d.masterSettingsPublisher.PublishFilesForce
	}
	res, err := publish(ctx, files)
	if err != nil {
		d.logger.Warn("failed to publish sealed master-settings replica", "error", err)
		return
	}
	if res.Changed {
		d.logger.Info("published sealed master-settings replica", "count", res.Files)
	} else {
		d.logger.Debug("sealed master-settings replica unchanged, publish skipped", "count", res.Files)
	}
}

// refreshSettingsState swaps the in-memory secret-encryption inputs (raw
// files, extracted secrets, top.zy) to match the given tree, and — when the
// extracted secret VALUES changed — fans re-encryption out to the fleet.
// Called by the lease holder after every settings publish AND by a standby's
// settings mirror after every sync (a facts-secrets-lease-holding standby
// must never encrypt stale secrets).
//
// The fleet fan-out is essential because a rotated !encrypted VALUE changes
// only the plaintext, not the sanitized bytes — hash-gate-invisible — and
// stable peels' facts publishes hash-skip, so nothing else would ever
// deliver the rotation. Per-peel PublishSecrets stays hash-gated.
func (d *Daemon) refreshSettingsState(ctx context.Context, files map[string][]byte, secrets settings.FileSecrets) {
	var topFile *settings.TopFile
	if topData, ok := files["top.zy"]; ok {
		var err error
		if topFile, err = settings.ParseTopFile(topData); err != nil {
			d.logger.Warn("failed to parse top.zy for secret filtering", "error", err)
		}
	}
	d.settingsMu.Lock()
	secretsChanged := secretsFingerprint(d.allSecrets) != secretsFingerprint(secrets)
	d.rawSettingsFiles = files
	d.allSecrets = secrets
	if topFile != nil {
		d.topFile = topFile
	}
	d.settingsMu.Unlock()

	// Fan re-encryption out ASYNCHRONOUSLY: this runs under the mirror's
	// syncMu (via OnSynced), and a whole-fleet re-encrypt must not block a
	// takeover's Quiesce. The in-memory state above is already refreshed
	// synchronously; the fan-out is idempotent and per-peel hash-gated.
	if secretsChanged {
		fanCtx := d.runCtx
		if fanCtx == nil {
			fanCtx = ctx
		}
		go d.republishSecretsToFleet(fanCtx)
	}
}

// refreshSettingsStateFromDisk re-reads the settings dir and refreshes the
// in-memory state — the settings mirror's OnSynced hook.
func (d *Daemon) refreshSettingsStateFromDisk() {
	if d.cfg == nil {
		return
	}
	ctx := d.runCtx
	if ctx == nil { // bare unit-test daemons
		ctx = context.Background()
	}
	files, err := loadSettingsFiles(d.cfg.SettingsDir)
	if err != nil {
		d.logger.Warn("settings mirror: reload after sync failed", "error", err)
		return
	}
	d.refreshSettingsState(ctx, files, extractFileSecrets(files, d.logger))
}

// secretsFingerprint returns a deterministic digest of the extracted
// secrets: sorted (file, key, value) triples, length-prefixed. Used to
// detect secret VALUE rotations, which the sanitized-content hash-gate
// cannot see (placeholders derive from keys alone).
func secretsFingerprint(fs settings.FileSecrets) string {
	if len(fs) == 0 {
		return ""
	}
	files := make([]string, 0, len(fs))
	for f := range fs {
		files = append(files, f)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		keys := make([]string, 0, len(fs[f]))
		for k := range fs[f] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := fs[f][k]
			fmt.Fprintf(h, "%d:%s%d:%s%d:%s", len(f), f, len(k), k, len(v), v)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// extractFileSecrets sanitizes each settings file in-memory and returns the
// per-file plaintext secrets — the same extraction PublishRawFiles performs
// when the lease holder publishes, minus the KV writes. Files that fail to
// sanitize are skipped with a warning (the leader's publish surfaces the
// same error).
func extractFileSecrets(files map[string][]byte, logger *slog.Logger) settings.FileSecrets {
	all := make(settings.FileSecrets)
	for key, data := range files {
		sanitized, err := settings.SanitizeFile(data)
		if err != nil {
			logger.Warn("failed to extract secrets from settings file", "file", key, "error", err)
			continue
		}
		if len(sanitized.Secrets) > 0 {
			all[key] = sanitized.Secrets
		}
	}
	return all
}

// loadSettingsFiles walks the settings directory and loads all .zy files.
// Returns a map of relative path (using / separator) to file content.
func loadSettingsFiles(dir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".zy" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("rel path for %s: %w", path, err)
		}
		// Normalize to forward slashes for KV keys.
		key := filepath.ToSlash(rel)
		files[key] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk settings dir %s: %w", dir, err)
	}
	return files, nil
}
