// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"nvpair-shared/appdir"
)

// manualNodeFile is the manual-node list's file name in the per-user data dir.
const manualNodeFile = "manual-nodes.json"

// persistedManualNode is one saved node/add request. The fields mirror the
// manual-nodes service's ManualEntry, so an entry replays as the exact request
// that created it.
type persistedManualNode struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	TLSPort int    `json:"tls_port,omitempty"`
	MTLS    bool   `json:"mtls,omitempty"`
}

// id is the key nvpair-manual-nodes gives the entry (its nodeID), which is also
// what node/remove names.
func (e persistedManualNode) id() string {
	if e.Name != "" {
		return e.Name
	}
	return "manual:" + e.Address
}

// manualNodeStore keeps the user's manual nodes across restarts.
// nvpair-manual-nodes holds them in memory only, and the desktop app's own
// replay (manual-nodes-store.ts) does not exist for a headless or TUI run, so
// the broker owns the durable list: it records each accepted node/add and
// node/remove and re-adds the list whenever it starts the manual-nodes worker.
// A nil store is a no-op, which keeps tests and an unresolved data dir
// in-memory only.
type manualNodeStore struct {
	mu   sync.Mutex
	path string
}

// newManualNodeStore resolves the store's file. A data dir that cannot be
// resolved yields nil rather than an error, since this is best-effort.
func newManualNodeStore() *manualNodeStore {
	p, err := appdir.Path(manualNodeFile)
	if err != nil {
		slog.Warn("could not resolve manual-nodes path; manual nodes will not survive a restart", "err", err)
		return nil
	}
	return &manualNodeStore{path: p}
}

// list returns the saved entries. A missing or unreadable file is an empty list.
func (s *manualNodeStore) list() []persistedManualNode {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *manualNodeStore) readLocked() []persistedManualNode {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not read manual nodes", "path", s.path, "err", err)
		}
		return nil
	}
	var entries []persistedManualNode
	if err := json.Unmarshal(raw, &entries); err != nil {
		slog.Warn("manual nodes file is not valid; ignoring it", "path", s.path, "err", err)
		return nil
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.Address != "" {
			kept = append(kept, e)
		}
	}
	return kept
}

// add saves an entry, replacing one with the same id so a repeated node/add
// updates it instead of duplicating it.
func (s *manualNodeStore) add(e persistedManualNode) {
	if s == nil || e.Address == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.readLocked()
	replaced := false
	for i := range entries {
		if entries[i].id() == e.id() {
			entries[i] = e
			replaced = true
		}
	}
	if !replaced {
		entries = append(entries, e)
	}
	s.writeLocked(entries)
}

// remove drops the entry with the given id.
func (s *manualNodeStore) remove(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.readLocked()
	kept := entries[:0]
	for _, e := range entries {
		if e.id() != id {
			kept = append(kept, e)
		}
	}
	if len(kept) != len(entries) {
		s.writeLocked(kept)
	}
}

// writeLocked replaces the file atomically, so a crash mid-write cannot leave a
// truncated list behind.
func (s *manualNodeStore) writeLocked(entries []persistedManualNode) {
	if entries == nil {
		entries = []persistedManualNode{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		slog.Warn("could not create data dir for manual nodes", "err", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".manual-nodes-*.tmp")
	if err != nil {
		slog.Warn("could not save manual nodes", "err", err)
		return
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.path)
	}
	if werr != nil {
		slog.Warn("could not save manual nodes", "path", s.path, "err", werr)
	}
}

// recordManualNodeRequest saves the effect of a node/add or node/remove that
// nvpair-manual-nodes accepted. It parses the client's own params, so the saved
// entry is what the client asked for.
func (b *Broker) recordManualNodeRequest(method string, params json.RawMessage) {
	switch method {
	case "node/add":
		var e persistedManualNode
		if json.Unmarshal(params, &e) == nil {
			b.manualStore.add(e)
		}
	case "node/remove":
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(params, &p) == nil {
			b.manualStore.remove(p.ID)
		}
	}
}

// replayManualNodes re-adds the saved nodes to a freshly started
// nvpair-manual-nodes. It talks to the worker directly, so replaying never
// rewrites the file. It runs off the caller's goroutine, because node/add
// starts an initial probe that can take a while against an unreachable node.
func (b *Broker) replayManualNodes(w *rpcWorker) {
	entries := b.manualStore.list()
	if len(entries) == 0 {
		return
	}
	go func() {
		for _, e := range entries {
			raw, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, rpcErr, err := w.CallNoTimeout(context.Background(), "node/add", raw); err != nil {
				slog.Warn("replay manual node failed", "address", e.Address, "err", err)
			} else if rpcErr != nil {
				slog.Warn("replay manual node rejected", "address", e.Address, "msg", rpcErr.Message)
			}
		}
		slog.Info("replayed saved manual nodes", "count", len(entries))
	}()
}
