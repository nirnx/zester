package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"

	"github.com/nirnx/zester/pkg/bus"
	"github.com/nirnx/zester/pkg/enroll"
)

var enrollCmd = &cobra.Command{
	Use:   "enroll",
	Short: "Manage peel enrollment",
	Long: `Manage peel enrollment requests.

Admin state transitions (approve, reject, revoke) are sent to a running
master via authenticated NATS request/reply; the master applies them to the
enrollment KV bucket. Read-only operations (list, show) read the KV bucket
directly.

If no master is running, pass --direct-kv to write the enrollment KV
directly with the CLI's own credentials (break-glass path).`,
}

func init() {
	enrollCmd.AddCommand(enrollListCmd)
	enrollCmd.AddCommand(enrollShowCmd)
	enrollCmd.AddCommand(enrollApproveCmd)
	enrollCmd.AddCommand(enrollRejectCmd)
	enrollCmd.AddCommand(enrollRevokeCmd)

	enrollCmd.PersistentFlags().Bool("direct-kv", false,
		"break-glass: write the enrollment KV directly instead of asking a master (use when no master is running)")

	rootCmd.AddCommand(enrollCmd)
}

// enrollAdminTimeout bounds one enrollment admin operation (request/reply
// round trip, or the direct KV transition).
const enrollAdminTimeout = 5 * time.Second

// enrollStore creates an enrollment store via the CLI's NATS connection.
// The admin's nkey/JWT credentials authenticate the NATS connection,
// so KV operations inherit the same trust boundary as all other CLI commands.
func enrollStore(ctx context.Context) (*enroll.Store, *bus.Client, error) {
	client, err := connectClient()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to NATS: %w", err)
	}

	store, err := enroll.NewStore(ctx, enroll.StoreConfig{
		JS: client.JetStream(),
	})
	if err != nil {
		client.Shutdown(ctx)
		return nil, nil, fmt.Errorf("open enrollment store: %w", err)
	}

	return store, client, nil
}

// runEnrollAdmin performs one enrollment admin state transition. The default
// path is request/reply on the given bus.SubjectAdminEnroll* subject,
// answered by a master's admin queue group; with --direct-kv it falls back
// to writing the enrollment KV bucket directly (break-glass for when no
// master is running).
func runEnrollAdmin(cmd *cobra.Command, subject, action, enrollmentID, reason string) (*enroll.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), enrollAdminTimeout)
	defer cancel()

	force, _ := cmd.Flags().GetBool("force")

	if directKV, _ := cmd.Flags().GetBool("direct-kv"); directKV {
		return runEnrollAdminKV(ctx, action, enrollmentID, reason, force)
	}

	client, err := connectClient()
	if err != nil {
		return nil, fmt.Errorf("connect to NATS: %w", err)
	}
	defer client.Shutdown(context.Background())

	req := enroll.AdminRequest{
		ID:       enrollmentID,
		Operator: currentUsername(),
		Reason:   reason,
		Force:    force,
	}
	var resp enroll.AdminResponse
	if err := client.Request(ctx, subject, &req, &resp); err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, fmt.Errorf("%s enrollment %s: no master is answering enrollment admin requests; retry with --direct-kv to write the enrollment KV directly: %w", action, enrollmentID, err)
		}
		return nil, fmt.Errorf("%s enrollment %s: %w", action, enrollmentID, err)
	}
	if err := resp.AsError(); err != nil {
		return nil, fmt.Errorf("%s enrollment %s: %w", action, enrollmentID, err)
	}
	if resp.Record == nil {
		return nil, fmt.Errorf("%s enrollment %s: master returned an empty record", action, enrollmentID)
	}
	return resp.Record, nil
}

// runEnrollAdminKV is the --direct-kv path: the state transition is applied
// straight to the enrollment KV bucket with the CLI's own credentials, as
// all admin operations were before the request/reply service existed.
func runEnrollAdminKV(ctx context.Context, action, enrollmentID, reason string, force bool) (*enroll.Record, error) {
	store, client, err := enrollStore(ctx)
	if err != nil {
		return nil, err
	}
	defer client.Shutdown(ctx)

	var rec *enroll.Record
	switch action {
	case "approve":
		rec, err = store.ApproveForce(ctx, enrollmentID, currentUsername(), force)
	case "reject":
		rec, err = store.Reject(ctx, enrollmentID, currentUsername(), reason)
	case "revoke":
		rec, err = store.Revoke(ctx, enrollmentID, currentUsername(), reason)
	default:
		return nil, fmt.Errorf("unknown enrollment admin action %q", action)
	}
	if err != nil {
		return nil, fmt.Errorf("%s enrollment %s: %w", action, enrollmentID, err)
	}
	return rec, nil
}
