// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestManualStore(t *testing.T) *manualNodeStore {
	t.Helper()
	return &manualNodeStore{path: filepath.Join(t.TempDir(), "manual-nodes.json")}
}

func TestManualNodeStoreSurvivesRestart(t *testing.T) {
	s := newTestManualStore(t)
	s.add(persistedManualNode{Address: "100.64.0.7", Name: "x1"})
	s.add(persistedManualNode{Address: "10.0.0.9"})

	// A new store on the same file is a restarted broker.
	got := (&manualNodeStore{path: s.path}).list()
	if len(got) != 2 || got[0].id() != "x1" || got[1].id() != "manual:10.0.0.9" {
		t.Fatalf("saved entries = %+v", got)
	}
}

func TestManualNodeStoreReplacesByID(t *testing.T) {
	s := newTestManualStore(t)
	s.add(persistedManualNode{Address: "100.64.0.7", Name: "x1"})
	s.add(persistedManualNode{Address: "100.64.0.8", Name: "x1"})
	got := s.list()
	if len(got) != 1 || got[0].Address != "100.64.0.8" {
		t.Fatalf("a repeated add should update the entry, got %+v", got)
	}
}

func TestManualNodeStoreRemove(t *testing.T) {
	s := newTestManualStore(t)
	s.add(persistedManualNode{Address: "100.64.0.7", Name: "x1"})
	s.add(persistedManualNode{Address: "10.0.0.9"})
	s.remove("x1")
	got := s.list()
	if len(got) != 1 || got[0].Address != "10.0.0.9" {
		t.Fatalf("after remove = %+v", got)
	}
	s.remove("manual:10.0.0.9")
	if got := s.list(); len(got) != 0 {
		t.Fatalf("expected an empty list, got %+v", got)
	}
}

func TestManualNodeStoreIgnoresBadFile(t *testing.T) {
	s := newTestManualStore(t)
	if err := os.WriteFile(s.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.list(); len(got) != 0 {
		t.Fatalf("a corrupt file should read as empty, got %+v", got)
	}
	s.add(persistedManualNode{Address: "100.64.0.7"})
	if got := s.list(); len(got) != 1 {
		t.Fatalf("a corrupt file should be replaced on the next save, got %+v", got)
	}
}

func TestNilManualNodeStoreIsNoop(t *testing.T) {
	var s *manualNodeStore
	s.add(persistedManualNode{Address: "100.64.0.7"})
	s.remove("x")
	if s.list() != nil {
		t.Fatal("nil store must list nothing")
	}
}

func TestRecordManualNodeRequest(t *testing.T) {
	b := &Broker{manualStore: newTestManualStore(t)}
	add, _ := json.Marshal(persistedManualNode{Address: "100.64.0.7", Name: "x1", TLSPort: 14319})
	b.recordManualNodeRequest("node/add", add)
	if got := b.manualStore.list(); len(got) != 1 || got[0].TLSPort != 14319 {
		t.Fatalf("add not recorded: %+v", got)
	}
	b.recordManualNodeRequest("nodes/list", nil) // not a mutation
	b.recordManualNodeRequest("node/remove", json.RawMessage(`{"id":"x1"}`))
	if got := b.manualStore.list(); len(got) != 0 {
		t.Fatalf("remove not recorded: %+v", got)
	}
}
