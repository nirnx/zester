package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const liveMetaPath = "../../website/content/docs/guides/modules/meta.json"

func TestRenderMetaJSON_ByteExactToLiveFile(t *testing.T) {
	got, err := renderMetaJSON()
	if err != nil {
		t.Fatalf("renderMetaJSON: %v", err)
	}
	want, err := os.ReadFile(liveMetaPath)
	if err != nil {
		t.Fatalf("read live meta.json: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("renderMetaJSON diverges from the live meta.json.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderMetaJSON_IndexNotListed(t *testing.T) {
	got, err := renderMetaJSON()
	if err != nil {
		t.Fatalf("renderMetaJSON: %v", err)
	}
	if bytes.Contains(got, []byte(`"index"`)) {
		t.Error("meta.json must never list \"index\" (§8) — Fumadocs includes it implicitly")
	}
}

func TestRenderMetaJSON_ValidJSON(t *testing.T) {
	got, err := renderMetaJSON()
	if err != nil {
		t.Fatalf("renderMetaJSON: %v", err)
	}
	var m modulesMeta
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("renderMetaJSON output is not valid JSON: %v", err)
	}
	if m.Title != "Modules" {
		t.Errorf("Title = %q, want Modules", m.Title)
	}
}

func TestAllPageSlugs_MatchLiveDirectory(t *testing.T) {
	dir := filepath.Dir(liveMetaPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read modules dir: %v", err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".mdx" {
			continue
		}
		slug := name[:len(name)-len(".mdx")]
		if slug == "index" {
			continue
		}
		onDisk[slug] = true
	}
	inMeta := map[string]bool{}
	for _, s := range allPageSlugs() {
		inMeta[s] = true
	}
	for s := range onDisk {
		if !inMeta[s] {
			t.Errorf("page %q exists on disk but is not listed in any family", s)
		}
	}
	for s := range inMeta {
		if !onDisk[s] {
			t.Errorf("page %q is listed in a family but has no .mdx file on disk", s)
		}
	}
}
