package config

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// bindNested exercises tagged fields inside an untagged nested struct.
type bindNested struct {
	Remotes  []string      `flag:"gitfs-remotes" usage:"Comma-separated Git remote URLs"`
	Interval time.Duration `flag:"gitfs-interval" usage:"GitFS pull interval"`
	SSHKey   string        // untagged: never bound
}

// bindConfig covers every supported type plus nesting and skipped fields.
type bindConfig struct {
	NatsURL  string        `flag:"nats-url" usage:"NATS server URL"`
	Replicas int           `flag:"jetstream-replicas" usage:"JetStream replication factor"`
	MaxBytes int64         `flag:"max-bytes" usage:"Max bytes"`
	Docs     bool          `flag:"api-docs" usage:"Serve docs"`
	Timeout  time.Duration `flag:"timeout" usage:"Request timeout"`
	Ratio    float64       `flag:"ratio" usage:"A ratio"`
	Skipped  string        `flag:"-"`
	Plain    string        // untagged: never bound
	GitFS    bindNested    // untagged struct: recursed into
}

func newBindConfig() bindConfig {
	return bindConfig{
		NatsURL:  "tls://nats:4222",
		Replicas: 0,
		MaxBytes: 1024,
		Docs:     false,
		Timeout:  5 * time.Minute,
		Ratio:    0.5,
		Skipped:  "keep",
		Plain:    "keep",
		GitFS: bindNested{
			Interval: 5 * time.Minute,
		},
	}
}

func newFlagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// bind + parse + optional "config file" mutation + apply, in the daemons' order.
func bindParseApply(t *testing.T, cfg *bindConfig, args []string, fileLoad func(*bindConfig)) {
	t.Helper()
	fs := newFlagSet(t)
	if err := BindFlags(fs, cfg); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	if fileLoad != nil {
		fileLoad(cfg) // simulates the YAML config file overwriting the struct
	}
	if err := ApplyVisited(fs, cfg); err != nil {
		t.Fatalf("ApplyVisited: %v", err)
	}
}

func TestBindFlagsRegistersDefaultsFromStruct(t *testing.T) {
	cfg := newBindConfig()
	fs := newFlagSet(t)
	if err := BindFlags(fs, &cfg); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}

	tests := []struct {
		name, defValue, usage string
	}{
		{"nats-url", "tls://nats:4222", "NATS server URL"},
		{"jetstream-replicas", "0", "JetStream replication factor"},
		{"max-bytes", "1024", "Max bytes"},
		{"api-docs", "false", "Serve docs"},
		{"timeout", "5m0s", "Request timeout"},
		{"ratio", "0.5", "A ratio"},
		{"gitfs-remotes", "", "Comma-separated Git remote URLs"},
		{"gitfs-interval", "5m0s", "GitFS pull interval"},
	}
	for _, tt := range tests {
		f := fs.Lookup(tt.name)
		if f == nil {
			t.Errorf("flag %q not registered", tt.name)
			continue
		}
		if f.DefValue != tt.defValue {
			t.Errorf("flag %q default = %q, want %q", tt.name, f.DefValue, tt.defValue)
		}
		if f.Usage != tt.usage {
			t.Errorf("flag %q usage = %q, want %q", tt.name, f.Usage, tt.usage)
		}
	}
	for _, name := range []string{"Skipped", "Plain", "SSHKey", "-"} {
		if fs.Lookup(name) != nil {
			t.Errorf("flag %q should not be registered", name)
		}
	}
}

// --- Precedence matrix ---

func TestPrecedenceDefaultOnly(t *testing.T) {
	cfg := newBindConfig()
	bindParseApply(t, &cfg, nil, nil)
	if got, want := cfg, newBindConfig(); !reflect.DeepEqual(got, want) {
		t.Errorf("defaults changed with no flags and no file:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestPrecedenceFileOnly(t *testing.T) {
	cfg := newBindConfig()
	bindParseApply(t, &cfg, nil, func(c *bindConfig) {
		c.NatsURL = "tls://file:4222"
		c.GitFS.Remotes = []string{"git@file:repo.git"}
	})
	if cfg.NatsURL != "tls://file:4222" {
		t.Errorf("NatsURL = %q, want file value", cfg.NatsURL)
	}
	if !reflect.DeepEqual(cfg.GitFS.Remotes, []string{"git@file:repo.git"}) {
		t.Errorf("Remotes = %v, want file value", cfg.GitFS.Remotes)
	}
}

func TestPrecedenceFlagOnly(t *testing.T) {
	cfg := newBindConfig()
	bindParseApply(t, &cfg, []string{"--nats-url", "tls://flag:4222", "--timeout", "30s"}, nil)
	if cfg.NatsURL != "tls://flag:4222" {
		t.Errorf("NatsURL = %q, want flag value", cfg.NatsURL)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", cfg.Timeout)
	}
	// Untouched fields keep struct defaults.
	if cfg.Replicas != 0 || cfg.Ratio != 0.5 {
		t.Errorf("untouched fields changed: %+v", cfg)
	}
}

func TestPrecedenceFlagOverFile(t *testing.T) {
	cfg := newBindConfig()
	bindParseApply(t, &cfg,
		[]string{"--nats-url", "tls://flag:4222"},
		func(c *bindConfig) {
			c.NatsURL = "tls://file:4222"
			c.Replicas = 3
		})
	if cfg.NatsURL != "tls://flag:4222" {
		t.Errorf("NatsURL = %q, want flag to beat file", cfg.NatsURL)
	}
	if cfg.Replicas != 3 {
		t.Errorf("Replicas = %d, want file value 3 (flag not passed)", cfg.Replicas)
	}
}

func TestPrecedenceExplicitEmptyStringFlagOverFile(t *testing.T) {
	// The gitfs-remotes case: --gitfs-remotes "" must clear a YAML value.
	cfg := newBindConfig()
	bindParseApply(t, &cfg,
		[]string{"--gitfs-remotes", "", "--nats-url", ""},
		func(c *bindConfig) {
			c.GitFS.Remotes = []string{"git@file:repo.git"}
			c.NatsURL = "tls://file:4222"
		})
	if len(cfg.GitFS.Remotes) != 0 {
		t.Errorf("Remotes = %v, want explicit empty flag to clear file value", cfg.GitFS.Remotes)
	}
	if cfg.NatsURL != "" {
		t.Errorf("NatsURL = %q, want explicit empty flag to clear file value", cfg.NatsURL)
	}
}

// --- Every supported type round-trips through parse + apply ---

func TestApplyVisitedAllTypes(t *testing.T) {
	cfg := newBindConfig()
	bindParseApply(t, &cfg, []string{
		"--nats-url", "tls://x:4222",
		"--jetstream-replicas", "5",
		"--max-bytes", "9223372036854775807",
		"--api-docs=true",
		"--timeout", "1h30m",
		"--ratio", "2.25",
		"--gitfs-remotes", "git@a:one.git, git@b:two.git ,,",
		"--gitfs-interval", "45s",
	}, nil)

	if cfg.NatsURL != "tls://x:4222" {
		t.Errorf("string: %q", cfg.NatsURL)
	}
	if cfg.Replicas != 5 {
		t.Errorf("int: %d", cfg.Replicas)
	}
	if cfg.MaxBytes != 9223372036854775807 {
		t.Errorf("int64: %d", cfg.MaxBytes)
	}
	if !cfg.Docs {
		t.Error("bool: want true")
	}
	if cfg.Timeout != 90*time.Minute {
		t.Errorf("duration: %v", cfg.Timeout)
	}
	if cfg.Ratio != 2.25 {
		t.Errorf("float64: %v", cfg.Ratio)
	}
	want := []string{"git@a:one.git", "git@b:two.git"}
	if !reflect.DeepEqual(cfg.GitFS.Remotes, want) {
		t.Errorf("[]string: %v, want %v (trimmed, empties dropped)", cfg.GitFS.Remotes, want)
	}
	if cfg.GitFS.Interval != 45*time.Second {
		t.Errorf("nested duration: %v", cfg.GitFS.Interval)
	}
}

func TestNestedStructFlags(t *testing.T) {
	type inner struct {
		Addr string `flag:"enroll-addr" usage:"listen address"`
	}
	type outer struct {
		Enroll inner
	}
	cfg := outer{Enroll: inner{Addr: ":8443"}}
	fs := newFlagSet(t)
	if err := BindFlags(fs, &cfg); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}
	if f := fs.Lookup("enroll-addr"); f == nil || f.DefValue != ":8443" {
		t.Fatalf("nested flag not registered with struct default: %+v", f)
	}
	if err := fs.Parse([]string{"--enroll-addr", ":9443"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := ApplyVisited(fs, &cfg); err != nil {
		t.Fatalf("ApplyVisited: %v", err)
	}
	if cfg.Enroll.Addr != ":9443" {
		t.Errorf("Enroll.Addr = %q, want :9443", cfg.Enroll.Addr)
	}
}

func TestApplyVisitedIgnoresUnboundFlags(t *testing.T) {
	// Flags registered outside BindFlags (e.g. --config) must be ignored.
	cfg := newBindConfig()
	fs := newFlagSet(t)
	var configFile string
	fs.StringVar(&configFile, "config", "", "Path to YAML config file")
	if err := BindFlags(fs, &cfg); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}
	if err := fs.Parse([]string{"--config", "/etc/zester/master.yaml"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := ApplyVisited(fs, &cfg); err != nil {
		t.Fatalf("ApplyVisited: %v", err)
	}
	if configFile != "/etc/zester/master.yaml" {
		t.Errorf("config flag = %q", configFile)
	}
}

// --- Error cases ---

func TestBindFlagsDuplicateNameInStruct(t *testing.T) {
	type dup struct {
		A string `flag:"same"`
		B int    `flag:"same"`
	}
	err := BindFlags(newFlagSet(t), &dup{})
	if err == nil || !strings.Contains(err.Error(), "duplicate flag name") || !strings.Contains(err.Error(), "same") {
		t.Fatalf("want duplicate-name error naming the flag, got %v", err)
	}
}

func TestBindFlagsDuplicateNameAcrossNesting(t *testing.T) {
	type inner struct {
		A string `flag:"same"`
	}
	type dup struct {
		B     string `flag:"same"`
		Inner inner
	}
	err := BindFlags(newFlagSet(t), &dup{})
	if err == nil || !strings.Contains(err.Error(), "duplicate flag name") {
		t.Fatalf("want duplicate-name error, got %v", err)
	}
}

func TestBindFlagsNameCollidesWithPreRegisteredFlag(t *testing.T) {
	type c struct {
		A string `flag:"config"`
	}
	fs := newFlagSet(t)
	fs.String("config", "", "pre-registered")
	err := BindFlags(fs, &c{})
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("want already-registered error, got %v", err)
	}
}

func TestBindFlagsUnsupportedType(t *testing.T) {
	type bad struct {
		M map[string]string `flag:"m"`
	}
	err := BindFlags(newFlagSet(t), &bad{})
	if err == nil || !strings.Contains(err.Error(), "unsupported flag type") {
		t.Fatalf("want unsupported-type error, got %v", err)
	}
	// The same structural error must come from ApplyVisited.
	if err := ApplyVisited(newFlagSet(t), &bad{}); err == nil || !strings.Contains(err.Error(), "unsupported flag type") {
		t.Fatalf("ApplyVisited: want unsupported-type error, got %v", err)
	}
}

func TestBindFlagsTaggedStructIsUnsupported(t *testing.T) {
	type inner struct{ A string }
	type bad struct {
		I inner `flag:"inner"`
	}
	err := BindFlags(newFlagSet(t), &bad{})
	if err == nil || !strings.Contains(err.Error(), "unsupported flag type") {
		t.Fatalf("want unsupported-type error for tagged struct, got %v", err)
	}
}

func TestBindFlagsNonPointer(t *testing.T) {
	err := BindFlags(newFlagSet(t), newBindConfig())
	if err == nil || !strings.Contains(err.Error(), "non-nil pointer to a struct") {
		t.Fatalf("want non-pointer error, got %v", err)
	}
}

func TestBindFlagsNilPointer(t *testing.T) {
	var cfg *bindConfig
	err := BindFlags(newFlagSet(t), cfg)
	if err == nil || !strings.Contains(err.Error(), "non-nil pointer to a struct") {
		t.Fatalf("want nil-pointer error, got %v", err)
	}
}

func TestBindFlagsPointerToNonStruct(t *testing.T) {
	s := "x"
	err := BindFlags(newFlagSet(t), &s)
	if err == nil || !strings.Contains(err.Error(), "point to a struct") {
		t.Fatalf("want pointer-to-non-struct error, got %v", err)
	}
	var applyErr = ApplyVisited(newFlagSet(t), &s)
	if applyErr == nil {
		t.Fatal("ApplyVisited: want pointer-to-non-struct error")
	}
}

func TestUnexportedFieldsSkipped(t *testing.T) {
	type c struct {
		Exported   string `flag:"exported"`
		unexported string //nolint:unused // presence is the point
	}
	fs := newFlagSet(t)
	if err := BindFlags(fs, &c{}); err != nil {
		t.Fatalf("BindFlags: %v", err)
	}
	if fs.Lookup("exported") == nil {
		t.Error("exported field should be bound")
	}
	if n := countFlags(fs); n != 1 {
		t.Errorf("registered %d flags, want 1", n)
	}
}

func countFlags(fs *flag.FlagSet) int {
	n := 0
	fs.VisitAll(func(*flag.Flag) { n++ })
	return n
}

// TestBindMasterDaemonConfigShape sanity-checks the binder against a real
// config struct from this package (no tags yet, so nothing binds — it must
// simply walk without error, including the nested Enroll/API/GitFS structs).
func TestBindMasterDaemonConfigShape(t *testing.T) {
	cfg := MasterDaemonDefaults()
	fs := newFlagSet(t)
	if err := BindFlags(fs, &cfg); err != nil {
		t.Fatalf("BindFlags(MasterDaemonConfig): %v", err)
	}
	if err := ApplyVisited(fs, &cfg); err != nil {
		t.Fatalf("ApplyVisited(MasterDaemonConfig): %v", err)
	}
}
