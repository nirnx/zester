package bustest

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/ptorbus/zester/pkg/bus"
)

func TestMatchSubject(t *testing.T) {
	tests := []struct {
		pattern, subject string
		want             bool
	}{
		{"a.b", "a.b", true},
		{"a.b", "a.c", false},
		{"a.*", "a.b", true},
		{"a.*", "a.b.c", false},
		{"a.>", "a.b", true},
		{"a.>", "a.b.c", true},
		{"a.>", "a", false}, // > requires at least one token
		{">", "a.b", true},
		{"*.b", "a.b", true},
		{"zester.job.*.schedule.*", "zester.job.j1.schedule.web-01", true},
		{"zester.job.*.schedule.*", "zester.job.j1.return.web-01", false},
	}
	for _, tt := range tests {
		if got := matchSubject(tt.pattern, tt.subject); got != tt.want {
			t.Errorf("matchSubject(%q, %q) = %v, want %v", tt.pattern, tt.subject, got, tt.want)
		}
	}
}

func TestFakeKVListKeysFiltered(t *testing.T) {
	ctx := context.Background()

	drain := func(t *testing.T, lister bus.KeyLister) []string {
		t.Helper()
		var keys []string
		for k := range lister.Keys() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	kv := NewFakeKV("test", 0)
	for _, k := range []string{"jid1", "jid1.peelA", "jid1.peelB", "jid2.peelA", "active.j1"} {
		if _, err := kv.Put(ctx, k, []byte("x")); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}

	t.Run("descendant wildcard excludes bare parent", func(t *testing.T) {
		lister, err := kv.ListKeysFiltered(ctx, "jid1.>")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		got := drain(t, lister)
		want := []string{"jid1.peelA", "jid1.peelB"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("multiple filters union", func(t *testing.T) {
		lister, err := kv.ListKeysFiltered(ctx, "jid2.>", "active.>")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		got := drain(t, lister)
		want := []string{"active.j1", "jid2.peelA"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("no filters lists all", func(t *testing.T) {
		lister, err := kv.ListKeysFiltered(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if got := drain(t, lister); len(got) != 5 {
			t.Errorf("got %v, want all 5 keys", got)
		}
	})

	t.Run("empty result is ErrNoKeysFound", func(t *testing.T) {
		_, err := kv.ListKeysFiltered(ctx, "nope.>")
		if !errors.Is(err, bus.ErrNoKeysFound) {
			t.Errorf("got %v, want bus.ErrNoKeysFound", err)
		}
	})

	t.Run("deleted keys excluded", func(t *testing.T) {
		if err := kv.Delete(ctx, "jid1.peelB"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		lister, err := kv.ListKeysFiltered(ctx, "jid1.>")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		got := drain(t, lister)
		if len(got) != 1 || got[0] != "jid1.peelA" {
			t.Errorf("got %v, want [jid1.peelA]", got)
		}
	})
}
