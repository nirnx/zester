package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ptorbus/zester/pkg/auth"
	"github.com/ptorbus/zester/pkg/bus"
)

const (
	authDir     = "/data/auth"
	natsDir     = "/data/nats"
	statesDir   = "/data/states"
	settingsDir = "/data/settings"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	logger.Info("playground-init: starting bootstrap")

	if err := run(logger); err != nil {
		logger.Error("bootstrap failed", "error", err)
		os.Exit(1)
	}

	logger.Info("playground-init: bootstrap complete")
}

func run(logger *slog.Logger) error {
	// Ensure auth and nats directories exist.
	if err := os.MkdirAll(authDir, 0755); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	if err := os.MkdirAll(natsDir, 0755); err != nil {
		return fmt.Errorf("create nats dir: %w", err)
	}

	// Generate key bundles.
	logger.Info("generating auth hierarchy")
	operatorKP, err := auth.GenerateKeyBundle(auth.RoleOperator)
	if err != nil {
		return fmt.Errorf("generate operator: %w", err)
	}

	accountKP, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		return fmt.Errorf("generate account: %w", err)
	}

	sysAccountKP, err := auth.GenerateKeyBundle(auth.RoleAccount)
	if err != nil {
		return fmt.Errorf("generate system account: %w", err)
	}

	// Create operator JWT with system account reference.
	operatorJWT, err := auth.CreateOperatorJWT(operatorKP, auth.OperatorJWTOptions{
		Name:          "zester-op",
		SystemAccount: sysAccountKP.PublicKey,
	})
	if err != nil {
		return fmt.Errorf("create operator JWT: %w", err)
	}

	// Create the zester account JWT with JetStream enabled.
	accountJWT, err := auth.CreateAccountJWT(accountKP, operatorKP, auth.AccountJWTOptions{
		Name:      "zester-acct",
		JetStream: true,
	})
	if err != nil {
		return fmt.Errorf("create account JWT: %w", err)
	}

	// Create the system account JWT (required by NATS for JetStream in operator mode).
	sysAccountJWT, err := auth.CreateAccountJWT(sysAccountKP, operatorKP, auth.AccountJWTOptions{
		Name: "SYS",
	})
	if err != nil {
		return fmt.Errorf("create system account JWT: %w", err)
	}

	// Write operator JWT.
	opJWTPath := filepath.Join(authDir, "operator.jwt")
	if err := os.WriteFile(opJWTPath, []byte(operatorJWT), 0644); err != nil {
		return fmt.Errorf("write operator JWT: %w", err)
	}
	logger.Info("wrote operator JWT", "path", opJWTPath)

	// Write account JWT.
	accJWTPath := filepath.Join(authDir, "account.jwt")
	if err := os.WriteFile(accJWTPath, []byte(accountJWT), 0644); err != nil {
		return fmt.Errorf("write account JWT: %w", err)
	}
	logger.Info("wrote account JWT", "path", accJWTPath)

	// Save account seed so master can reload it.
	accountSeedPath := filepath.Join(authDir, "account.seed")
	if err := accountKP.SaveSeedToFile(accountSeedPath); err != nil {
		return fmt.Errorf("save account seed: %w", err)
	}
	logger.Info("saved account seed", "path", accountSeedPath)

	// Generate master.creds for the master service.
	masterUserKP, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		return fmt.Errorf("generate master user key: %w", err)
	}
	masterOpts := auth.MasterUserJWTOptions(accountKP.PublicKey)
	masterJWT, err := auth.CreateUserJWT(masterUserKP, accountKP, masterOpts)
	if err != nil {
		return fmt.Errorf("create master user JWT: %w", err)
	}
	masterCredsPath := filepath.Join(authDir, "master.creds")
	if err := auth.WriteCredsFile(masterCredsPath, masterJWT, masterUserKP.Seed); err != nil {
		return fmt.Errorf("write master creds: %w", err)
	}
	logger.Info("generated master credentials", "path", masterCredsPath)

	// Generate admin.creds for the CLI tool.
	adminUserKP, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		return fmt.Errorf("generate admin user key: %w", err)
	}
	adminOpts := auth.AdminUserJWTOptions(accountKP.PublicKey)
	adminJWT, err := auth.CreateUserJWT(adminUserKP, accountKP, adminOpts)
	if err != nil {
		return fmt.Errorf("create admin user JWT: %w", err)
	}
	adminCredsPath := filepath.Join(authDir, "admin.creds")
	if err := auth.WriteCredsFile(adminCredsPath, adminJWT, adminUserKP.Seed); err != nil {
		return fmt.Errorf("write admin creds: %w", err)
	}
	logger.Info("generated admin credentials", "path", adminCredsPath)

	// Generate TLS certificates for the enrollment HTTPS API.
	if err := generateEnrollmentTLS(authDir, logger); err != nil {
		return fmt.Errorf("generate enrollment TLS: %w", err)
	}

	// Generate TLS certificates for NATS client transport.
	if err := generateNATSTLS(authDir, logger); err != nil {
		return fmt.Errorf("generate NATS TLS: %w", err)
	}

	// Write nats-server.conf for the external NATS server.
	natsConf := fmt.Sprintf(`port: 4222
tls {
  cert_file: /data/auth/nats-server.crt
  key_file: /data/auth/nats-server.key
}
jetstream {
  store_dir: /data/jetstream
}
operator: /data/auth/operator.jwt
system_account: %s
resolver: MEMORY
resolver_preload: {
  %s: %s
  %s: %s
}
`, sysAccountKP.PublicKey, accountKP.PublicKey, accountJWT, sysAccountKP.PublicKey, sysAccountJWT)
	natsConfPath := filepath.Join(natsDir, "nats-server.conf")
	if err := os.WriteFile(natsConfPath, []byte(natsConf), 0644); err != nil {
		return fmt.Errorf("write nats-server.conf: %w", err)
	}
	logger.Info("wrote nats-server.conf", "path", natsConfPath)

	// Write zester CLI config so the admin container can run enrollment
	// commands without manual --master/--creds flags.
	cliConfig := `master:
  urls: ["tls://nats:4222"]
  creds_file: /data/auth/admin.creds
  tls_ca: /data/auth/nats-ca.crt
`
	cliConfigPath := filepath.Join(natsDir, "zester.yaml")
	if err := os.WriteFile(cliConfigPath, []byte(cliConfig), 0644); err != nil {
		return fmt.Errorf("write CLI config: %w", err)
	}
	logger.Info("wrote CLI config", "path", cliConfigPath)

	// Generate a random REST API token for integration/admin automation.
	// Consumers read it from the shared auth volume, so it never needs to
	// be a predictable value.
	apiTokenDir := filepath.Join(authDir, "api-tokens")
	if err := os.MkdirAll(apiTokenDir, 0755); err != nil {
		return fmt.Errorf("create api token dir: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate API token: %w", err)
	}
	apiTokenPath := filepath.Join(apiTokenDir, "integration.token")
	if err := os.WriteFile(apiTokenPath, []byte(hex.EncodeToString(tokenBytes)+"\n"), 0600); err != nil {
		return fmt.Errorf("write API token: %w", err)
	}
	logger.Info("wrote API token", "path", apiTokenPath)

	// Write master daemon config: TLS NATS connection plus the REST API
	// token mapping (docs enabled for playground convenience).
	masterConfig := `nats_url: "tls://nats:4222"
nats_ca: "/data/auth/nats-ca.crt"
api:
  docs_enabled: true
  tokens:
    - username: "integration-test"
      token_file: "/data/auth/api-tokens/integration.token"
`
	masterConfigPath := filepath.Join(natsDir, "master.yaml")
	if err := os.WriteFile(masterConfigPath, []byte(masterConfig), 0644); err != nil {
		return fmt.Errorf("write master config: %w", err)
	}
	logger.Info("wrote master config", "path", masterConfigPath)

	// Copy example states to shared volume.
	if err := copyDir("/playground/states", statesDir); err != nil {
		return fmt.Errorf("copy states: %w", err)
	}
	logger.Info("copied states", "dst", statesDir)

	// Copy example settings to shared volume.
	if err := copyDir("/playground/settings", settingsDir); err != nil {
		return fmt.Errorf("copy settings: %w", err)
	}
	logger.Info("copied settings", "dst", settingsDir)

	return nil
}

func generateEnrollmentTLS(authDir string, logger *slog.Logger) error {
	ca, err := bus.GenerateSelfSignedCA("Zester Playground", 10*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("generate CA: %w", err)
	}

	certPEM, keyPEM, err := ca.IssueCert("master", nil, []string{"master", "localhost"}, 10*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("issue server cert: %w", err)
	}

	caPath := filepath.Join(authDir, "enroll-ca.crt")
	if err := bus.WritePEM(caPath, ca.CertPEM); err != nil {
		return fmt.Errorf("write CA cert: %w", err)
	}
	logger.Info("wrote enrollment CA", "path", caPath)

	certPath := filepath.Join(authDir, "enroll.crt")
	if err := bus.WritePEM(certPath, certPEM); err != nil {
		return fmt.Errorf("write server cert: %w", err)
	}
	logger.Info("wrote enrollment cert", "path", certPath)

	keyPath := filepath.Join(authDir, "enroll.key")
	if err := bus.WritePEM(keyPath, keyPEM); err != nil {
		return fmt.Errorf("write server key: %w", err)
	}
	logger.Info("wrote enrollment key", "path", keyPath)

	return nil
}

func generateNATSTLS(authDir string, logger *slog.Logger) error {
	ca, err := bus.GenerateSelfSignedCA("Zester NATS", 10*365*24*time.Hour)
	if err != nil {
		return fmt.Errorf("generate NATS CA: %w", err)
	}

	certPEM, keyPEM, err := ca.IssueCert(
		"nats",
		nil,
		[]string{"nats", "nats-2", "nats-3", "localhost"},
		10*365*24*time.Hour,
	)
	if err != nil {
		return fmt.Errorf("issue NATS server cert: %w", err)
	}

	caPath := filepath.Join(authDir, "nats-ca.crt")
	if err := bus.WritePEM(caPath, ca.CertPEM); err != nil {
		return fmt.Errorf("write NATS CA cert: %w", err)
	}
	logger.Info("wrote NATS CA", "path", caPath)

	certPath := filepath.Join(authDir, "nats-server.crt")
	if err := bus.WritePEM(certPath, certPEM); err != nil {
		return fmt.Errorf("write NATS server cert: %w", err)
	}
	logger.Info("wrote NATS server cert", "path", certPath)

	keyPath := filepath.Join(authDir, "nats-server.key")
	if err := bus.WritePEM(keyPath, keyPEM); err != nil {
		return fmt.Errorf("write NATS server key: %w", err)
	}
	logger.Info("wrote NATS server key", "path", keyPath)

	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
