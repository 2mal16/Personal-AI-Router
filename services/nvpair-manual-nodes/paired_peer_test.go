// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"testing"

	"nvpair-shared/clustertrust"
	"nvpair-shared/clustertrusttest"
)

// httpsOnly wraps a fake handler so a plaintext request fails the test: a paired
// peer's proxies refuse plaintext from anywhere but loopback.
func httpsOnly(t *testing.T, body string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			t.Errorf("%s probed over %s, want https", r.URL, r.URL.Scheme)
		}
		return httpJSON(http.StatusOK, body)
	}
}

// probeOnce tracks entry and runs a single probe, returning the resulting status.
func probeOnce(m *Manager, entry ManualEntry) ManualNodeStatus {
	id := nodeID(entry)
	m.mu.Lock()
	m.nodes[id] = &trackedNode{entry: entry}
	m.mu.Unlock()
	m.probeNode(entry)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodes[id].status
}

// TestPairedPeerProbedOverMTLS covers a paired PAIR node added by hand because
// mDNS doesn't cross the network between them (a VPN such as WireGuard or
// Tailscale). Its node-info reports a cluster principal this node holds a pin
// for, so its proxies are probed over cluster mTLS and its per-engine inventory,
// including llama-swap, comes from its engine-manager.
func TestPairedPeerProbedOverMTLS(t *testing.T) {
	const peerUUID = "principal-vpn-peer"
	const addr = "peer.tailnet.ts.net"
	clusterDir := filepath.Join(t.TempDir(), "cluster")
	clustertrusttest.Join(t, clusterDir, "cluster-xyz", "principal-self", peerUUID)

	m, _, rt := newTestManager()
	m.mesh = clustertrust.Open(clusterDir)
	peerRT := newFakeRoundTripper()
	m.peerTransport = func(*tls.Config) http.RoundTripper { return peerRT }

	rt.set(http.MethodGet, net.JoinHostPort(addr, "14318"), "/v1/node-info", func(*http.Request) (*http.Response, error) {
		data, _ := json.Marshal(NodeInfoResponse{HostUUID: peerUUID, ClusterUUID: peerUUID})
		return httpJSON(http.StatusOK, string(data))
	})
	ollamaHost := net.JoinHostPort(addr, "11434")
	peerRT.set(http.MethodGet, ollamaHost, "/", httpsOnly(t, `{}`))
	peerRT.set(http.MethodGet, ollamaHost, "/api/tags", httpsOnly(t, `{"models":[{"name":"qwen3:8b"}]}`))
	peerRT.set(http.MethodGet, net.JoinHostPort(addr, "1234"), "/v1/models",
		httpsOnly(t, `{"data":[{"id":"swap-model"},{"id":"lm-model"}]}`))
	peerRT.set(http.MethodGet, net.JoinHostPort(addr, "14322"), "/v1/models",
		httpsOnly(t, `{"modelsByEngine":{"ollama":["qwen3:8b"],"lmstudio":["lm-model"],"llama-swap":["swap-model"]}}`))

	status := probeOnce(m, ManualEntry{Address: addr, Name: "agent"})

	if !status.Trusted || status.ClusterUUID != peerUUID {
		t.Fatalf("trusted=%v clusterUuid=%q, want a trusted peer %q", status.Trusted, status.ClusterUUID, peerUUID)
	}
	if !status.OllamaUp || !slices.Equal(status.OllamaModels, []string{"qwen3:8b"}) {
		t.Fatalf("ollama up=%v models=%v", status.OllamaUp, status.OllamaModels)
	}
	if !status.LMStudioUp || !slices.Equal(status.LMStudioModels, []string{"swap-model", "lm-model"}) {
		t.Fatalf("lmstudio up=%v models=%v", status.LMStudioUp, status.LMStudioModels)
	}
	if got := status.ModelsByEngine["llama-swap"]; !slices.Equal(got, []string{"swap-model"}) {
		t.Fatalf("llama-swap models = %v", got)
	}
}

// TestUnpinnedClusterNodeProbedInPlaintext keeps the pin as the gate: a node in
// a cluster this node holds no pin for is probed in plaintext as before, and is
// neither trusted nor dialed with this node's cluster identity.
func TestUnpinnedClusterNodeProbedInPlaintext(t *testing.T) {
	const addr = "stranger.local"
	clusterDir := filepath.Join(t.TempDir(), "cluster")
	clustertrusttest.Join(t, clusterDir, "cluster-xyz", "principal-self", "principal-ourpeer")

	m, _, rt := newTestManager()
	m.mesh = clustertrust.Open(clusterDir)
	m.peerTransport = func(*tls.Config) http.RoundTripper {
		t.Fatal("unpinned node dialed with the cluster identity")
		return nil
	}
	configureHealthyNode(rt, addr, []string{"llama3.2"}, NodeInfoResponse{HostUUID: "stranger", ClusterUUID: "principal-stranger"})

	status := probeOnce(m, ManualEntry{Address: addr})

	if status.Trusted || status.ModelsByEngine != nil {
		t.Fatalf("trusted=%v modelsByEngine=%v, want an untrusted plain node", status.Trusted, status.ModelsByEngine)
	}
	if !status.OllamaUp || !slices.Equal(status.OllamaModels, []string{"llama3.2"}) {
		t.Fatalf("ollama up=%v models=%v", status.OllamaUp, status.OllamaModels)
	}
}
