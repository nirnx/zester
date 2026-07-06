package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/auth"
	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/facts"
)

var peelCmd = &cobra.Command{
	Use:   "peel",
	Short: "Manage peels (managed nodes)",
}

var peelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List connected peels",
	RunE:  runPeelList,
}

var peelInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate nkey seed for a new peel",
	RunE:  runPeelInit,
}

func init() {
	peelCmd.AddCommand(peelListCmd)
	peelCmd.AddCommand(peelInitCmd)

	peelInitCmd.Flags().StringP("output", "o", "", "output file for the seed (default: stdout)")
}

func runPeelList(cmd *cobra.Command, args []string) error {
	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketFacts)
	if err != nil {
		return fmt.Errorf("get facts bucket: %w", err)
	}

	keys, err := listKVKeys(ctx, kv)
	if err != nil {
		return fmt.Errorf("list peel facts: %w", err)
	}

	if len(keys) == 0 {
		fmt.Println("No peels found.")
		return nil
	}

	// Liveness comes from the peel-heartbeat bucket (30s-TTL keys the peel
	// daemon rewrites every 10s), not from mere presence in the facts bucket.
	// Old fleets without the bucket degrade gracefully: both columns show "-".
	hbKV, hbErr := bus.GetBucket(ctx, client.JetStream(), bus.BucketPeelHeartbeat)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PEEL ID\tOS\tARCH\tONLINE\tLAST-SEEN")
	for _, key := range keys {
		var peelFacts map[string]any
		if err := bus.KVGet(ctx, kv, key, &peelFacts); err != nil {
			continue
		}
		osName := nestedFactStr(peelFacts, "os", "name")
		arch := nestedFactStr(peelFacts, "os", "arch")

		online, lastSeen := "-", "-"
		if hbErr == nil {
			var hb facts.Heartbeat
			err := bus.KVGet(ctx, hbKV, key, &hb)
			online, lastSeen = heartbeatStatus(&hb, err == nil, time.Now())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", key, osName, arch, online, lastSeen)
	}
	w.Flush()
	return nil
}

// heartbeatFreshWindow is the maximum heartbeat age still considered live.
// The peel-heartbeat bucket has a 30s TTL and peels beat every 10s, so key
// presence alone already implies liveness; this timestamp check is a
// defensive backstop (lagging TTL enforcement, clock skew) at 2x the TTL.
const heartbeatFreshWindow = 60 * time.Second

// heartbeatStatus derives the ONLINE and LAST-SEEN column values from a
// peel's heartbeat record. found reports whether the heartbeat key existed
// at all; absent means offline (the TTL expired the key).
func heartbeatStatus(hb *facts.Heartbeat, found bool, now time.Time) (online, lastSeen string) {
	if !found {
		return "no", "-"
	}
	if hb.TS.IsZero() {
		// Present but no timestamp (unexpected writer): presence within a
		// TTL'd bucket still implies liveness.
		return "yes", "-"
	}
	age := now.Sub(hb.TS)
	if age < 0 {
		age = 0
	}
	lastSeen = age.Round(time.Second).String() + " ago"
	if age > heartbeatFreshWindow {
		return "no", lastSeen
	}
	return "yes", lastSeen
}

func runPeelInit(cmd *cobra.Command, args []string) error {
	bundle, err := auth.GenerateKeyBundle(auth.RoleUser)
	if err != nil {
		return fmt.Errorf("generate nkey: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output != "" {
		if err := bundle.SaveSeedToFile(output); err != nil {
			return fmt.Errorf("save seed: %w", err)
		}
		fmt.Printf("Peel nkey seed saved to: %s\n", output)
		fmt.Printf("Public key: %s\n", bundle.PublicKey)
		return nil
	}

	fmt.Printf("Public key: %s\n", bundle.PublicKey)
	fmt.Printf("Seed:       %s\n", string(bundle.Seed))
	fmt.Println("\nSave the seed to /etc/zester/peel.key and set permissions to 0600.")
	return nil
}

func connectClient() (*bus.Client, error) {
	urls := bus.NormalizeNATSURLs(masterURLs())
	if err := bus.ValidateTLSNATSURLs(urls); err != nil {
		return nil, fmt.Errorf("rejecting NATS configuration: %w", err)
	}

	cc := bus.ClientConfig{
		URLs:          urls,
		Name:          "zester",
		MaxReconnects: 1,
		ReconnectWait: time.Second,
		Logger:        slog.New(slog.DiscardHandler),
	}

	if cfg != nil {
		// --creds flag overrides config file.
		if creds, _ := rootCmd.Flags().GetString("creds"); creds != "" {
			cc.CredsFile = creds
		} else if cfg.Master.CredsFile != "" {
			cc.CredsFile = cfg.Master.CredsFile
		}

		if cfg.Master.NKeySeedFile != "" {
			cc.NKeySeedFile = cfg.Master.NKeySeedFile
		}

		tlsCfg, err := buildTLSConfig(cfg.Master.TLSCert, cfg.Master.TLSKey, cfg.Master.TLSCA)
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
		cc.TLS = tlsCfg
	}

	return bus.NewClient(cc)
}

func buildTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" && caFile == "" {
		return nil, nil
	}

	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("both tls_cert and tls_key must be set together")
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsCfg.RootCAs = pool
	}

	return tlsCfg, nil
}

func listKVKeys(ctx context.Context, kv bus.KV) ([]string, error) {
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	var keys []string
	for key := range lister.Keys() {
		keys = append(keys, key)
	}
	return keys, nil
}

func factStr(facts map[string]any, key string) string {
	if v, ok := facts[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return "-"
}

func nestedFactStr(facts map[string]any, keys ...string) string {
	var current any = facts
	for _, k := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return "-"
		}
		current, ok = m[k]
		if !ok {
			return "-"
		}
	}
	if current == nil {
		return "-"
	}
	return fmt.Sprintf("%v", current)
}
