package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nirnx/zester/internal/config"
)

const (
	peelBinPath      = "/usr/local/bin/zester-peel"
	masterBinPath    = "/usr/local/bin/zester-master"
	peelConfigPath   = "/etc/zester/peel.yaml"
	masterConfigPath = "/etc/zester/master.yaml"
	// bootstrapCacheName mirrors internal/peeld's on-disk cache filename.
	bootstrapCacheName = "nats-bootstrap.msgpack"
)

// deriveComponentDefaults fills every watchdog flag the operator did NOT set
// explicitly from --component and the child's config file, so a packaged unit
// can be just `zester-watchdog --component peel`. Explicit flags always win
// (`explicit` is the set of user-set flag names from flag.Visit). The peel and
// watchdog resolve their id from the same source (peel.yaml, else hostname), so
// they can never disagree about the node's identity or its <id>.creds filename.
func deriveComponentDefaults(f *watchdogFlags, explicit map[string]bool, logger *slog.Logger) error {
	var binDefault, cfgDefault string
	isPeel := f.component == "peel"
	switch f.component {
	case "peel":
		binDefault, cfgDefault = peelBinPath, peelConfigPath
	case "master":
		binDefault, cfgDefault = masterBinPath, masterConfigPath
	default:
		return fmt.Errorf("unknown --component %q (want peel or master)", f.component)
	}

	if !explicit["child-bin"] {
		f.childBin = binDefault
	}
	if !explicit["child-args"] {
		f.childArgs = deriveChildArgs(cfgDefault)
	}

	// Read the child's config (the operator may point --child-args at a custom
	// --config) to derive auth_dir / data_dir / health_addr / nats_ca / id.
	// A missing config is not fatal — the child may run purely on flags — so
	// fall back to the per-component defaults.
	childArgList := shellSplit(f.childArgs)
	cfgPath := configPathFromArgs(childArgList, cfgDefault)
	var authDir, dataDir, healthAddr, natsCA, cfgID string
	if isPeel {
		pc := config.PeelDefaults()
		if fileExists(cfgPath) {
			if loaded, err := config.LoadPeel(cfgPath); err != nil {
				logger.Warn("watchdog: could not read peel config; using defaults", "path", cfgPath, "error", err)
			} else {
				pc = *loaded
			}
		}
		authDir, dataDir, healthAddr, natsCA, cfgID = pc.AuthDir, pc.DataDir, pc.HealthAddr, pc.NatsCA, pc.ID
	} else {
		mc := config.MasterDaemonDefaults()
		if fileExists(cfgPath) {
			if loaded, err := config.LoadMasterDaemon(cfgPath); err != nil {
				logger.Warn("watchdog: could not read master config; using defaults", "path", cfgPath, "error", err)
			} else {
				mc = *loaded
			}
		}
		authDir, healthAddr, natsCA = mc.AuthDir, mc.HealthAddr, mc.NatsCA
	}

	// The child applies its own CLI flags OVER its config (flag > YAML), so
	// any identity-relevant flag the operator put in --child-args must win
	// here too — otherwise the watchdog would derive creds/health-url from
	// values the child does not actually use.
	overlay := func(name string, dst *string) {
		if v, ok := argValue(childArgList, name); ok {
			*dst = v
		}
	}
	overlay("auth-dir", &authDir)
	overlay("health-addr", &healthAddr)
	overlay("nats-ca", &natsCA)
	if isPeel {
		overlay("data-dir", &dataDir)
		overlay("id", &cfgID)
	}

	// Node id: an explicit --id wins; otherwise the peel config id (master has
	// none -> the pinned identity, else hostname). BOTH paths pass through
	// ResolveNodeID so the id is sanitized and validated exactly like the
	// child's — a dotted --id (e.g. an FQDN) maps to the same web01_example_com
	// the peel resolves, keeping the <id>.creds derivation in agreement.
	if explicit["id"] {
		id, err := config.ResolveNodeID(f.id, authDir, logger)
		if err != nil {
			return err
		}
		f.id = id
	} else {
		id, err := config.ResolveNodeID(cfgID, authDir, logger)
		if err != nil {
			return err
		}
		f.id = id
	}

	if !explicit["nats-creds"] {
		credsName := "master.creds"
		if isPeel {
			credsName = f.id + ".creds"
		}
		f.natsCreds = filepath.Join(authDir, credsName)
	}
	if !explicit["nats-ca"] {
		if natsCA != "" {
			f.natsCA = natsCA
		} else {
			f.natsCA = filepath.Join(authDir, "nats-ca.crt")
		}
	}
	if isPeel && !explicit["bootstrap-cache"] {
		f.bootstrapCache = filepath.Join(dataDir, bootstrapCacheName)
	}
	// Health URL from the child's health_addr — fixes the footgun where a
	// master watchdog had to remember to override the default :9090 to :9091.
	if !explicit["health-url"] && healthAddr != "" {
		f.healthURL = "http://" + healthAddr + "/healthz"
	}
	return nil
}

// deriveChildArgs builds the default child arg list: --config with the
// packaged path, but only when that file exists. The child treats an
// EXPLICITLY passed missing config as fatal (LoadPeel/LoadMasterDaemon error
// on it), while with no flag it falls back to built-in defaults — so injecting
// the path unconditionally would crash-loop a config-less box that ran fine
// before.
func deriveChildArgs(cfgDefault string) string {
	if fileExists(cfgDefault) {
		return "--config " + cfgDefault
	}
	return ""
}

// configPathFromArgs finds the child's --config value in its arg list, falling
// back to def.
func configPathFromArgs(args []string, def string) string {
	if v, ok := argValue(args, "config"); ok {
		return v
	}
	return def
}

// argValue finds a flag's value in a child arg list. Handles the four stdlib
// flag spellings: "--name x", "-name x", "--name=x", "-name=x".
func argValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if (a == "--"+name || a == "-"+name) && i+1 < len(args) {
			return args[i+1], true
		}
		for _, pfx := range []string{"--" + name + "=", "-" + name + "="} {
			if v, ok := strings.CutPrefix(a, pfx); ok {
				return v, true
			}
		}
	}
	return "", false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
