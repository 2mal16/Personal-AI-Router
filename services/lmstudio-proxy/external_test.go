// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"nvpair-shared/clustertrust"
	"nvpair-shared/clustertrusttest"
	"nvpair-shared/noderec"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIEnginesRouteAndListIndependently(t *testing.T) {
	p := testProxy(NewDiscovery(), 1235)
	for _, engine := range []string{"lmstudio", "llama-swap"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/models" {
				json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": engine + "-model"}, {"id": "shared"}}})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"engine": engine})
		}))
		defer server.Close()
		u, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(u.Port())
		p.setLocalBackend(localBackend{Engine: engine, Port: port, Healthy: true, Models: []string{engine + "-model", "shared"}})
	}
	p.discovery.SetSubscribed([]Node{{ID: "self", Host: "127.0.0.1", Addresses: []string{"127.0.0.1"}, Port: 1235, Models: []string{"lmstudio-model", "llama-swap-model", "shared"}, ModelsByEngine: map[string][]string{"lmstudio": {"lmstudio-model", "shared"}, "llama-swap": {"llama-swap-model", "shared"}}}})
	for _, engine := range []string{"lmstudio", "llama-swap"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+engine+`-model","messages":[]}`))
		w := httptest.NewRecorder()
		p.handleHTTP(w, req)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), engine) {
			t.Fatalf("%s response = %d %s", engine, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	p.handleHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "llama-swap-model") || !strings.Contains(w.Body.String(), "lmstudio-model") {
		t.Fatalf("models = %d %s", w.Code, w.Body.String())
	}
	if got := p.resolveCandidates("shared"); len(got) != 1 || got[0].engineName() != "lmstudio" {
		t.Fatalf("duplicate model candidates = %+v", got)
	}
	p.setLocalBackend(localBackend{Engine: "llama-swap", Healthy: false})
	if got := p.resolveCandidates("llama-swap-model"); len(got) != 0 {
		t.Fatalf("stale inventory still routes to stopped engine: %+v", got)
	}
	if got := p.resolveCandidates("lmstudio-model"); len(got) != 1 {
		t.Fatal("stopping llama-swap removed LM Studio")
	}
}

func TestSubscribedLlamaSwapModelsDoNotIncludeOllama(t *testing.T) {
	node, ok := subscribedToNode(noderec.DirectoryNode{HostUUID: "peer", IP: "192.0.2.1", Services: map[noderec.ServiceKey]noderec.ServiceStatus{noderec.ServiceLMStudio: {Port: 1235}}, ModelsByEngine: map[string][]string{"ollama": {"ollama-only"}, "llama-swap": {"swap-only"}}})
	if !ok || len(node.Models) != 1 || node.Models[0] != "swap-only" || node.engineForModel("swap-only") != "llama-swap" {
		t.Fatalf("projection = %+v", node)
	}
}

func TestExternalPeerIngressPreservesStreamingAndTrust(t *testing.T) {
	clusterDir := t.TempDir()
	clustertrusttest.Join(t, clusterDir, "test-cluster", "self", "peer")
	raw, err := os.ReadFile(filepath.Join(clusterDir, "trusted", "peer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pin struct {
		CertPEM string `json:"certPem"`
	}
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(pin.CertPEM))
	if block == nil {
		t.Fatal("missing pinned certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), `"model":"swap-model"`) {
			t.Errorf("request body not preserved")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"delta\":\"test\"}\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	p := testProxy(NewDiscovery(), 1235)
	p.mesh = clustertrust.Open(clusterDir)
	p.setLocalBackend(localBackend{Engine: "llama-swap", Port: port, Healthy: true, Models: []string{"swap-model"}})
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"swap-model","stream":true,"messages":[]}`))
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
		return r
	}
	w := httptest.NewRecorder()
	p.handleClusterIngress(w, request())
	if w.Code != http.StatusOK || !w.Flushed || !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("stream = %d %s", w.Code, w.Body.String())
	}
	clustertrusttest.RemovePeerPin(t, clusterDir, "peer")
	w = httptest.NewRecorder()
	p.handleClusterIngress(w, request())
	if w.Code != http.StatusForbidden || requests.Load() != 1 {
		t.Fatal("removed peer reached external engine")
	}
}

// TestSelfCandidatesFollowTheEndpointThatAnswers pins the two attribution rules
// that only differ once LM Studio is gone: a self candidate reports the local
// engine that actually serves the request rather than the preferred owner of
// the model ID, and a request that names no model keeps both local engines as
// candidates, LM Studio first.
func TestSelfCandidatesFollowTheEndpointThatAnswers(t *testing.T) {
	p := testProxy(NewDiscovery(), 1235)
	ports := map[string]int{}
	for _, engine := range []string{"lmstudio", "llama-swap"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
		}))
		defer server.Close()
		u, _ := url.Parse(server.URL)
		port, _ := strconv.Atoi(u.Port())
		ports[engine] = port
		p.setLocalBackend(localBackend{Engine: engine, Port: port, Healthy: true, Models: []string{"shared"}})
	}
	p.discovery.SetSubscribed([]Node{{ID: "self", Host: "127.0.0.1", Addresses: []string{"127.0.0.1"}, Port: 1235, Models: []string{"shared"}, ModelsByEngine: map[string][]string{"lmstudio": {"shared"}, "llama-swap": {"shared"}}}})

	got := p.resolveCandidates("")
	if len(got) != 2 || got[0].engineName() != "lmstudio" || got[1].engineName() != "llama-swap" {
		t.Fatalf("model-less candidates = %+v, want both local engines with LM Studio first", got)
	}
	if got[0].url.Port() != strconv.Itoa(ports["lmstudio"]) || got[1].url.Port() != strconv.Itoa(ports["llama-swap"]) {
		t.Fatalf("candidate URLs = %v, %v", got[0].url, got[1].url)
	}

	// LM Studio stops. Discovery still attributes "shared" to it until the next
	// snapshot, but the request can only be answered by llama-swap, so the
	// workload has to name llama-swap.
	p.setLocalBackend(localBackend{Engine: "lmstudio", Healthy: false})
	got = p.resolveCandidates("shared")
	if len(got) != 1 || got[0].engineName() != "llama-swap" {
		t.Fatalf("candidates after LM Studio stopped = %+v, want llama-swap alone", got)
	}
}
