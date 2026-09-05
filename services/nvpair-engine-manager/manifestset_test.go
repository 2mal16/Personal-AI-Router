// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvpair-shared/engines"
)

// nvpair-shared/engines states that an engine's Name must match its manifest
// basename, and argues that declaring the set once turns a mismatch into a
// build error. For the broker, proxy and scheduler it does, because they all
// read the table. This component does not: it embeds manifests/*.json and
// never imports engines, so adding an engine to the table without a manifest
// (or the reverse) still compiles.
//
// The failure that produces is the one the package comment describes — an
// engine that discovers and advertises but never resolves a model owner —
// which is invisible until inference is attempted. The existing manifest tests
// validate content, not the set, so this closes the last gap.
func TestBundledManifestSet_MatchesEngineTable(t *testing.T) {
	entries, err := fs.ReadDir(bundledManifests, "manifests")
	require.NoError(t, err)

	have := map[string]bool{}
	for _, e := range entries {
		have[strings.TrimSuffix(e.Name(), ".json")] = true
	}

	for _, engine := range engines.All() {
		assert.Contains(t, have, engine.Name, "engine table entry has no bundled manifest")
		delete(have, engine.Name)
	}
	// An externally managed engine is observed, never launched, and rides the
	// facade of the engine whose dialect it speaks (llama-swap is served by the
	// lmstudio facade, see LocalEngines in nvpair-proxy's profile), so it has no
	// engines-table entry of its own: that table is what mints a facade, a port
	// and a discovery key. Any other manifest still needs one.
	for name := range have {
		if host, ok := externalEngineHosts[name]; ok {
			if _, hosted := engines.ByName(host); !hosted {
				t.Errorf("manifests/%s.json is hosted by %q, which is not in nvpair-shared/engines", name, host)
			}
			continue
		}
		t.Errorf("manifests/%s.json has no entry in nvpair-shared/engines", name)
	}
}

// externalEngineHosts maps each externally managed bundled engine to the engine
// whose proxy facade fronts it.
var externalEngineHosts = map[string]string{"llama-swap": "lmstudio"}
