package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
)

var kvCmd = &cobra.Command{
	Use:   "kv",
	Short: "Query KV store data (cached facts, settings)",
	Long: `Query data directly from the NATS KV store.

These commands read cached data from the KV store. For live peel queries,
use the direct execution syntax instead:

  zester kv fact get <peel> [key]     # reads cached facts from KV store
  zester '<peel>' facts.get <key>     # sends request to live peel`,
}

var kvFactCmd = &cobra.Command{
	Use:   "fact",
	Short: "Query peel facts from KV store",
}

var kvFactGetCmd = &cobra.Command{
	Use:   "get <peel> [key]",
	Short: "Get facts for a peel, optionally filtered by key",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runKVFactGet,
}

func init() {
	kvCmd.AddCommand(kvFactCmd)
	kvFactCmd.AddCommand(kvFactGetCmd)
}

func runKVFactGet(cmd *cobra.Command, args []string) error {
	// Accept the dotted human form: facts keys use the wire token
	// ('.' encoded as '_'), and a dotted id can never be a KV key.
	peelID := enroll.SanitizeGlobDots(args[0])

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

	var facts map[string]any
	if err := bus.KVGet(ctx, kv, peelID, &facts); err != nil {
		return fmt.Errorf("get facts for %s: %w", peelID, err)
	}

	if len(args) == 2 {
		key := args[1]
		val := lookupFactKey(facts, key)
		if val == nil {
			return fmt.Errorf("fact key %q not found for peel %s", key, peelID)
		}
		return kvPrintJSON(val)
	}

	return kvPrintJSON(facts)
}

func lookupFactKey(m map[string]any, key string) any {
	parts := splitDotKey(key)
	var current any = m
	for _, part := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = cm[part]
		if !ok {
			return nil
		}
	}
	return current
}

func splitDotKey(key string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			parts = append(parts, key[start:i])
			start = i + 1
		}
	}
	parts = append(parts, key[start:])
	return parts
}

func kvPrintJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
