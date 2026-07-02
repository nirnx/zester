package job

import (
	"context"
	"log/slog"
	"time"

	"github.com/ptorbus/zester/pkg/bus"
)

// ActiveJobKeyPrefix is the key prefix of the active-jobs index inside the
// jobs KV bucket. For every non-terminal claimed job "<jid>" there is a
// companion key "active.<jid>" holding a tiny ActiveJobEntry; the orphan
// scanner enumerates only these keys instead of the whole 7-day jobs
// bucket (O(active) instead of O(retained), architecture review finding 8).
//
// Anything that lists the jobs bucket by key (CLI job listing, master REST
// API) must skip keys with this prefix — they are index entries, not job
// records.
const ActiveJobKeyPrefix = "active."

// ActiveJobKey returns the active-index key for a JID ("active.<jid>").
func ActiveJobKey(jid string) string {
	return ActiveJobKeyPrefix + jid
}

// ActiveJobEntry is the tiny value stored under an active-index key. It
// deliberately carries only the fields the orphan scanner needs to decide
// whether the referenced job record is worth a full Get; the job record
// itself stays the source of truth for status/epoch/reclaim-count.
type ActiveJobEntry struct {
	// Owner is the master instance that currently owns the job.
	Owner string `msgpack:"owner"`

	// Updated is when the index entry was last written (claim or reclaim).
	Updated time.Time `msgpack:"updated"`
}

// writeActiveKey creates (create=true, at claim time) or rewrites
// (create=false, at reclaim time) the active-index key for a job. The index
// is best-effort: a failed write is logged and the job simply stays (or
// becomes) invisible to the orphan scanner — the 7-day bucket TTL
// eventually ages it out.
func writeActiveKey(ctx context.Context, kv bus.KV, jid, owner string, create bool, logger *slog.Logger) {
	entry := ActiveJobEntry{Owner: owner, Updated: time.Now().UTC()}
	data, err := bus.Encode(entry)
	if err != nil {
		logger.Warn("active-jobs index: encode entry", "jid", jid, "error", err)
		return
	}
	if create {
		if _, err := kv.Create(ctx, ActiveJobKey(jid), data); err != nil {
			logger.Warn("active-jobs index: create key", "jid", jid, "error", err)
		}
		return
	}
	if _, err := kv.Put(ctx, ActiveJobKey(jid), data); err != nil {
		logger.Warn("active-jobs index: rewrite key", "jid", jid, "error", err)
	}
}

// clearActiveKey deletes the active-index key for a job. Called on every
// terminal transition. Deletion failure is only logged: the orphan scanner
// self-heals stale index entries (active key referencing a terminal or
// missing job record) on its next pass.
func clearActiveKey(ctx context.Context, kv bus.KV, jid string, logger *slog.Logger) {
	if err := kv.Delete(ctx, ActiveJobKey(jid)); err != nil {
		logger.Warn("active-jobs index: delete key (scanner will self-heal)", "jid", jid, "error", err)
	}
}
