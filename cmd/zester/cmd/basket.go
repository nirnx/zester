package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/target"
)

var basketCmd = &cobra.Command{
	Use:   "basket",
	Short: "Query basket (peel-to-peel shared data)",
}

var basketGetCmd = &cobra.Command{
	Use:   "get <target> <function>",
	Short: "Get basket data from targeted peels",
	Args:  cobra.ExactArgs(2),
	RunE:  runBasketGet,
}

var basketListCmd = &cobra.Command{
	Use:   "list <peel>",
	Short: "List all basket keys for a peel",
	Args:  cobra.ExactArgs(1),
	RunE:  runBasketList,
}

func init() {
	basketCmd.AddCommand(basketGetCmd)
	basketCmd.AddCommand(basketListCmd)
}

func runBasketGet(cmd *cobra.Command, args []string) error {
	tgtExpr := args[0]
	function := args[1]

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Resolve targets.
	tt := target.DetectType(tgtExpr)
	lister := &target.KVPeelLister{JS: client.JetStream()}
	peels, err := target.Resolve(ctx, tgtExpr, tt, lister)
	if err != nil {
		return fmt.Errorf("resolve target %q: %w", tgtExpr, err)
	}

	if len(peels) == 0 {
		fmt.Println("No peels matched the target expression.")
		return nil
	}

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketBasket)
	if err != nil {
		return fmt.Errorf("get basket bucket: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PEEL\tVALUE")
	for _, peelID := range peels {
		key := peelID + "." + function
		var val any
		if err := bus.KVGet(ctx, kv, key, &val); err != nil {
			fmt.Fprintf(w, "%s\t(no data)\n", peelID)
			continue
		}
		fmt.Fprintf(w, "%s\t%v\n", peelID, val)
	}
	w.Flush()
	return nil
}

func runBasketList(cmd *cobra.Command, args []string) error {
	peelID := args[0]

	client, err := connectClient()
	if err != nil {
		return err
	}
	defer client.Shutdown(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	kv, err := bus.GetBucket(ctx, client.JetStream(), bus.BucketBasket)
	if err != nil {
		return fmt.Errorf("get basket bucket: %w", err)
	}

	keys, err := listKVKeys(ctx, kv)
	if err != nil {
		return fmt.Errorf("list basket keys: %w", err)
	}

	prefix := peelID + "."
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FUNCTION")
	found := false
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			function := key[len(prefix):]
			fmt.Fprintln(w, function)
			found = true
		}
	}
	w.Flush()

	if !found {
		fmt.Printf("No basket data found for peel %s.\n", peelID)
	}
	return nil
}
