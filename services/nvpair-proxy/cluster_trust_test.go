// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"nvpair-shared/clustertrust"
	"nvpair-shared/clustertrusttest"
	"nvpair-shared/engines"
)

// TestResolveCandidatesFollowsLivePinSet reproduces the join ordering that made
// a freshly-joined node route to nobody. The peer is discovered BEFORE this node
// has any cluster identity, and its discovery record never changes again — which
// is the normal steady state, because the relay only re-sends a node when its
// mDNS record actually moves. Routing must still pick the peer up the moment the
// pin lands, and drop it again the moment the pin is removed, because it reads
// the live pin set rather than a trust flag cached at discovery time.
func TestResolveCandidatesFollowsLivePinSet(t *testing.T) {
	const peerUUID = "principal-peer"
	clusterDir := filepath.Join(t.TempDir(), "cluster")

	disc := NewDiscovery()
	disc.SetSubscribed([]Node{{
		ID: "peer-a", Host: "peer-a", Port: 11434,
		Addresses:   []string{"192.0.2.10"},
		IP:          "192.0.2.10",
		ClusterUUID: peerUUID,
	}})
	p := testProxy(anyProfile(t), disc, 11435)
	p.mesh = clustertrust.Open(clusterDir)

	// Pre-join: no identity, no pins, so the peer is not a routable target.
	if got := p.soleFacade().resolveCandidates(""); len(got) != 0 {
		t.Fatalf("pre-join candidates = %+v, want none", got)
	}

	// The cluster-manager lands the join on disk while the proxy is running. No
	// new discovery snapshot arrives — the peer's mDNS record has not changed.
	clustertrusttest.Join(t, clusterDir, "cluster-xyz", "principal-self", peerUUID)

	cands := p.soleFacade().resolveCandidates("")
	if len(cands) != 1 {
		t.Fatalf("post-join candidates = %+v, want the peer", cands)
	}
	if cands[0].id != "peer-a" || cands[0].peerUUID != peerUUID || cands[0].url.Scheme != "https" {
		t.Fatalf("post-join candidate = %+v, want peer-a over https pinned to %s", cands[0], peerUUID)
	}

	// Removing the peer from the cluster retires it as a target just as promptly,
	// again with no discovery event involved.
	clustertrusttest.RemovePeerPin(t, clusterDir, peerUUID)
	if got := p.soleFacade().resolveCandidates(""); len(got) != 0 {
		t.Fatalf("post-removal candidates = %+v, want none", got)
	}
}

// TestResolveCandidatesRejectsUnpinnedClusteredPeer keeps the isolation property
// honest now that trust is read locally: a peer that advertises a cluster
// principal we hold no pin for is not routable, even though this node is itself
// a healthy cluster member. Two clusters on one LAN must not route to each other.
func TestResolveCandidatesRejectsUnpinnedClusteredPeer(t *testing.T) {
	clusterDir := filepath.Join(t.TempDir(), "cluster")
	clustertrusttest.Join(t, clusterDir, "cluster-ours", "principal-self", "principal-ourpeer")

	disc := NewDiscovery()
	disc.SetSubscribed([]Node{{
		ID: "stranger", Host: "stranger", Port: 11434,
		Addresses:   []string{"192.0.2.30"},
		IP:          "192.0.2.30",
		ClusterUUID: "principal-stranger",
	}})
	p := testProxy(anyProfile(t), disc, 11435)
	p.mesh = clustertrust.Open(clusterDir)

	if got := p.soleFacade().resolveCandidates(""); len(got) != 0 {
		t.Fatalf("candidates = %+v, want none for a peer in another cluster", got)
	}
}

// TestManualPairedPeerRoutesOverMTLS covers a paired peer added by hand because
// mDNS can't reach it (a VPN such as WireGuard or Tailscale). node/add-manual
// carries its cluster principal, so routing dials its proxy over cluster mTLS;
// without the principal, or without a pin for it, the entry stays a plain manual
// target.
func TestManualPairedPeerRoutesOverMTLS(t *testing.T) {
	const peerUUID = "principal-vpn-peer"
	clusterDir := filepath.Join(t.TempDir(), "cluster")
	clustertrusttest.Join(t, clusterDir, "cluster-xyz", "principal-self", peerUUID)

	for _, tc := range []struct {
		name        string
		clusterUUID string
		wantScheme  string
		wantPeer    string
	}{
		{name: "pinned", clusterUUID: peerUUID, wantScheme: "https", wantPeer: peerUUID},
		{name: "unpinned", clusterUUID: "principal-stranger", wantScheme: "http"},
		{name: "plain", wantScheme: "http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := testProxy(lmstudioProfile(t), NewDiscovery(), 1235)
			host.mesh = clustertrust.Open(clusterDir)
			p := host.soleFacade()
			params, _ := json.Marshal(map[string]any{
				"id": "vpn-peer", "host": "peer.tailnet.ts.net", "port": 1234,
				"addresses": []string{"192.0.2.40"}, "clusterUuid": tc.clusterUUID,
			})
			id := json.RawMessage(`1`)
			host.handleMessage(&Message{JSONRPC: "2.0", ID: &id, Method: engines.AddressMethod("lmstudio", "node/add-manual"), Params: params})

			cands := p.resolveCandidates("")
			if len(cands) != 1 {
				t.Fatalf("candidates = %+v, want the manual peer", cands)
			}
			if cands[0].url.Scheme != tc.wantScheme || cands[0].peerUUID != tc.wantPeer {
				t.Fatalf("candidate = %+v, want scheme %s pinned to %q", cands[0], tc.wantScheme, tc.wantPeer)
			}
		})
	}
}
