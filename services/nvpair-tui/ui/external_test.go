// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An externally managed engine is observed, never driven: every lifecycle key
// is refused on the spot, before any confirmation is armed, so nothing reaches
// the engine-manager to be refused there instead.
func TestExternalEngine_RefusesLifecycleKeys(t *testing.T) {
	for _, k := range []string{"i", "s", "x", "r", "u"} {
		t.Run(k, func(t *testing.T) {
			d := localDetail()
			d.engines = []engineStatus{{Engine: "llama-swap", DisplayName: "llama-swap", ExternallyManaged: true, Installed: true, Running: true}}
			d.refreshEngines()
			d.pane = detailEngines

			cmd := d.handleKeyForTest(t, k)

			assert.Nil(t, cmd, "sent a command for an externally managed engine")
			assert.Nil(t, d.pending, "armed a confirmation for an externally managed engine")
			assert.Contains(t, d.status.render(), "externally managed", "explanation given")
		})
	}
}

func TestExternalEngine_IsLabelledInTheEngineTable(t *testing.T) {
	d := localDetail()
	d.engines = []engineStatus{{Engine: "llama-swap", DisplayName: "llama-swap", ExternallyManaged: true, Installed: true}}

	d.refreshEngines()

	rows := d.engineTable.Rows()
	if assert.Len(t, rows, 1) {
		assert.Equal(t, "llama-swap (external)", rows[0][0])
	}
}

func TestExternalEngine_IsNeverADownloadTarget(t *testing.T) {
	d := localDetail()
	d.engines = []engineStatus{{Engine: "llama-swap", ExternallyManaged: true, Installed: true, Running: true}}
	d.refreshEngines()

	assert.Empty(t, d.pullTargetEngine(), "offered an externally managed engine as a download target")
}
