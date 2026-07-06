package masterd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/settings"
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

	// Publish master's curve public key so peels can decrypt secrets.
	// Deliberately NOT lease-gated: every master must be able to encrypt
	// secrets, and the key is identical across masters sharing account.seed.
	masterCurvePub := masterEnc.PublicKey()
	if _, err := secretsKV.Put(ctx, settings.MasterCurvePubKey, []byte(masterCurvePub)); err != nil {
		d.logger.Warn("failed to publish master curve public key", "error", err)
	} else {
		d.logger.Info("published master curve public key for peel decryption")
	}

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

// publishSettingsFiles sanitizes the loaded raw settings files and writes
// them to the shared settings-files bucket. Called only by the
// publisher-lease holder (runLeaderPublish); the extracted secrets already
// live in d.allSecrets from load time, so the return value is discarded.
func (d *Daemon) publishSettingsFiles(ctx context.Context) {
	if len(d.rawSettingsFiles) == 0 {
		return
	}
	if _, err := d.publisher.PublishRawFiles(ctx, d.rawSettingsFiles); err != nil {
		d.logger.Warn("failed to publish raw settings files", "error", err)
		return
	}
	d.logger.Info("published raw settings files for peel-side rendering",
		"files", len(d.rawSettingsFiles),
		"secrets", len(d.allSecrets),
	)
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
