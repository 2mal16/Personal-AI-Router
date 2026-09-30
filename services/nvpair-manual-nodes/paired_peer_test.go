// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nvpair-shared/noderec"
)

// httpsOnly wraps a fake handler so a plaintext request fails the test: a paired
// peer's proxies refuse plaintext from anywhere but loopback.
func httpsOnly(t *testing.T, body string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		assert.Equal(t, "https", r.URL.Scheme, "%s probed in plaintext", r.URL)
		return httpJSON(http.StatusOK, body)
	}
}

// pairedPeerEngines points the fixture's verified peer at fake engine endpoints
// on its fixed ports, reached only through the client pinned to its principal.
// It returns the principals the manager asked for a pinned client.
func pairedPeerEngines(t *testing.T, f *manualPeerFixture) *[]string {
	t.Helper()
	peerRT := newFakeRoundTripper()
	addr := f.entry.Address
	ollama := net.JoinHostPort(addr, "11434")
	peerRT.set(http.MethodGet, ollama, "/", httpsOnly(t, `{}`))
	peerRT.set(http.MethodGet, ollama, "/api/tags", httpsOnly(t, `{"models":[{"name":"qwen3:8b"}]}`))
	peerRT.set(http.MethodGet, net.JoinHostPort(addr, "1234"), "/v1/models",
		httpsOnly(t, `{"data":[{"id":"swap-model"},{"id":"lm-model"}]}`))
	peerRT.set(http.MethodGet, net.JoinHostPort(addr, "14322"), "/v1/models",
		httpsOnly(t, `{"modelsByEngine":{"ollama":["qwen3:8b"],"lmstudio":["lm-model"],"llama-swap":["swap-model"]}}`))
	var asked []string
	f.manager.enginePeerClient = func(uuid string) (*http.Client, bool) {
		asked = append(asked, uuid)
		return &http.Client{Transport: peerRT, Timeout: time.Second}, uuid == manualPeerUUID
	}
	return &asked
}

func probedStatus(t *testing.T, f *manualPeerFixture) ManualNodeStatus {
	t.Helper()
	f.manager.probeNode(f.entry)
	nodes := f.manager.listNodes()
	require.Len(t, nodes, 1)
	return nodes[0]
}

// A paired PAIR node added by hand because mDNS doesn't cross the network
// between them (a VPN such as WireGuard or Tailscale). Once node-info proves
// its identity, its proxies are probed over cluster mTLS pinned to that
// principal and its per-engine inventory, including llama-swap, comes from its
// engine-manager.
func TestPairedPeer_EnginesProbedOverPinnedMTLS(t *testing.T) {
	f := newManualPeerFixture(t)
	asked := pairedPeerEngines(t, f)

	status := probedStatus(t, f)

	assert.Equal(t, noderec.IdentityPinned, status.IdentityVerification)
	assert.Equal(t, []string{manualPeerUUID}, *asked, "engine probes pinned to the verified principal")
	assert.Equal(t, manualPeerUUID, status.ClusterUUID)
	assert.True(t, status.OllamaUp)
	assert.Equal(t, []string{"qwen3:8b"}, status.OllamaModels)
	assert.True(t, status.LMStudioUp)
	assert.Equal(t, []string{"swap-model", "lm-model"}, status.LMStudioModels)
	assert.Equal(t, []string{"swap-model"}, status.ModelsByEngine["llama-swap"])
}

// A failed node-info read is not proof, so the entry's engines go back to
// plaintext probes like any unverified configured target, and the principal
// and engine-manager inventory are dropped with it.
func TestPairedPeer_FailedVerificationProbesEnginesInPlaintext(t *testing.T) {
	f := newManualPeerFixture(t)
	asked := pairedPeerEngines(t, f)
	require.Equal(t, manualPeerUUID, probedStatus(t, f).ClusterUUID)

	f.respond = func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
	status := probedStatus(t, f)

	assert.False(t, status.NodeInfoUp)
	assert.Equal(t, []string{manualPeerUUID}, *asked, "asked for a pinned client without fresh proof")
	assert.Empty(t, status.ClusterUUID)
	assert.Nil(t, status.ModelsByEngine)
}

// An entry whose identity was never proved is probed in plaintext, and no
// pinned client is requested for it.
func TestUnverifiedEntry_EnginesProbedInPlaintext(t *testing.T) {
	const addr = "stranger.local"
	m, _, rt := newTestManager()
	asked := 0
	m.enginePeerClient = func(string) (*http.Client, bool) { asked++; return nil, false }
	configureHealthyOllama(t, rt, addr, []string{"llama3.2"})
	entry := ManualEntry{Address: addr}
	m.nodes[nodeID(entry)] = &trackedNode{entry: entry}

	m.probeNode(entry)

	nodes := m.listNodes()
	require.Len(t, nodes, 1)
	assert.Zero(t, asked, "asked for a pinned client without a verified principal")
	assert.Empty(t, nodes[0].ClusterUUID)
	assert.Nil(t, nodes[0].ModelsByEngine)
	assert.True(t, nodes[0].OllamaUp)
	assert.Equal(t, []string{"llama3.2"}, nodes[0].OllamaModels)
}
