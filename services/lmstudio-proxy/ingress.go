// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"

	"nvpair-shared/cors"
)

const engineIdentityProbeHeader = "X-NVPAIR-Engine-Identity-Probe"

// localBackend is the explicit loopback engine the cluster mTLS ingress
// forwards to. It is supplied by the broker over node/set-local-backend and is
// deliberately NOT sourced from the discovery overlay: a request that arrived
// over the LAN mTLS ingress can only ever be dumped on this node's own local
// engine, never re-routed to a peer, so the ingress path is strictly terminal
// and cannot recurse or amplify.
type localBackend struct {
	Engine  string   `json:"engine"`
	Host    string   `json:"host"`
	Port    int      `json:"port"`
	Healthy bool     `json:"healthy"`
	Models  []string `json:"models,omitempty"`
}

// setLocalBackend records (or, with a zero port / unhealthy flag, effectively
// clears) the local engine the ingress serves.
func (p *Proxy) setLocalBackend(b localBackend) {
	p.backendMu.Lock()
	if p.engines == nil {
		p.engines = make(map[string]localBackend)
	}
	if b.Engine == "" {
		b.Engine = "lmstudio"
	}
	// Port/liveness-only updates must not erase a known model inventory.
	if b.Models == nil {
		if previous, ok := p.engines[b.Engine]; ok && previous.Port == b.Port {
			b.Models = previous.Models
		} else {
			b.Models = []string{}
		}
	}
	p.engines[b.Engine] = b
	p.backendMu.Unlock()
}

// localEngineCandidates returns the loopback engines this machine has
// configured, in preference order, restricted to the ones whose inventory
// advertises model when one is named: LM Studio wins a duplicate model ID and
// llama-swap stays independently routable. An engine that is unset, has no
// port, or is unhealthy is not a candidate, so an empty result is what makes
// the ingress answer 503 rather than forward. The host defaults to 127.0.0.1
// and is always loopback.
func (p *Proxy) localEngineCandidates(model string) []candidate {
	p.backendMu.RLock()
	defer p.backendMu.RUnlock()
	var out []candidate
	for _, engine := range []string{"lmstudio", "llama-swap"} {
		b, ok := p.engines[engine]
		if !ok || b.Port <= 0 || !b.Healthy {
			continue
		}
		if model != "" && b.Models != nil && !slices.Contains(b.Models, model) {
			continue
		}
		host := b.Host
		if host == "" {
			host = "127.0.0.1"
		}
		out = append(out, candidate{engine: engine, url: &url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(b.Port))}})
	}
	return out
}

// handlePlain is the plaintext personality: it accepts requests only from
// loopback and hands them to the full local router (handleHTTP). A non-loopback
// caller — any LAN peer — is refused; peers must use the mTLS ingress. This is
// what closes the former open-relay exposure (the listener still binds all
// interfaces for the TLS personality, but plaintext is loopback-only).
func (p *Proxy) handlePlain(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRemote(r.RemoteAddr) {
		// Answer a non-loopback preflight ahead of the gate. It grants no access
		// on its own; the request that follows still receives the real 403. A
		// loopback preflight continues into handleHTTP so an available engine's
		// exact origin and credentials policy can be preserved.
		if cors.WritePreflight(w, r) {
			return
		}
		slog.Warn("rejected non-loopback plaintext request; cluster peers must use mTLS",
			"remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		writeIngressError(w, http.StatusForbidden, "loopback-only",
			"plaintext requests are accepted only from loopback; cluster peers must use the mTLS ingress")
		return
	}
	// Engine-manager marks identity/action requests so the federated model-list
	// facade can never satisfy LM Studio's own /v1/models readiness probe.
	if r.Header.Get(engineIdentityProbeHeader) == "1" {
		writeIngressError(w, http.StatusConflict, "proxy-facade", "the compatibility facade is not an LM Studio engine")
		return
	}
	p.handleHTTP(w, r)
}

// handleClusterIngress is the LAN mTLS personality: it authenticates the caller
// against this node's cluster pins and, once the peer is a trusted cluster
// member, forwards the request straight to the local loopback engine — exactly
// like the local plaintext path, with no route filtering. The mTLS pin is the
// sole authorization boundary (a trusted peer is treated like a local client),
// so the two personalities stay behaviorally identical toward the engine. It
// never calls resolveCandidates, so a peer request cannot be re-routed onward.
func (p *Proxy) handleClusterIngress(w http.ResponseWriter, r *http.Request) {
	// Re-derive membership and pins per request so a cluster left, or a peer
	// paired or removed, after startup is reflected immediately without a proxy
	// restart — a removed peer must stop being accepted right away, which is the
	// whole point of the gate.
	p.mesh.Refresh()
	peer, ok := p.mesh.VerifyClientPin(r)
	if !ok {
		writeIngressError(w, http.StatusForbidden, "cluster-auth",
			"client certificate is not a pinned member of this node's cluster")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		_, _ = p.serveModelList(w, r, p.localEngineCandidates(""))
		return
	}
	// Only an inference route needs its model, and reading it means buffering
	// the body. Everything else streams to the engine as it always has.
	model := ""
	if isInferenceRequest(r.Method, r.URL.Path) {
		body, parsed := bufferBodyAndModel(r)
		r.Body = io.NopCloser(bytes.NewReader(body))
		model = parsed
	}
	candidates := p.localEngineCandidates(model)
	if len(candidates) == 0 {
		writeIngressError(w, http.StatusServiceUnavailable, "no-local-backend",
			"no local inference backend is available on this node")
		return
	}
	target := candidates[0].url
	slog.Debug("cluster ingress forwarding to local backend",
		"peer", peer, "method", r.Method, "path", r.URL.Path, "target", target.Host)
	p.reverseProxyToLocal(w, r, target)
}

// reverseProxyToLocal streams the request to the local engine, preserving
// cancellation (the request context is the proxy's root context, so a client
// disconnect or shutdown tears down the upstream call and stops generation).
func (p *Proxy) reverseProxyToLocal(w http.ResponseWriter, r *http.Request, target *url.URL) {
	p.newLocalReverseProxy(target).ServeHTTP(w, r)
}

func (p *Proxy) newLocalReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		Transport: p.plainHTTPTransport(),
		ErrorHandler: func(ew http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("cluster ingress upstream error", "target", target.Host, "err", err)
			writeIngressError(ew, http.StatusBadGateway, "backend-error", "local inference backend error")
		},
	}
}

// isLoopbackRemote reports whether an http.Request RemoteAddr (host:port) is a
// loopback address (127.0.0.0/8 or ::1). An unparseable/empty RemoteAddr is not
// loopback, so it fails closed.
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// writeIngressError writes a small structured JSON error. It never echoes the
// request body or any generated output. CORS headers are included because these
// are the proxy's own rejections: without them a browser client cannot read the
// status or reason, and every one of them looks like a generic CORS failure.
func writeIngressError(w http.ResponseWriter, status int, code, msg string) {
	cors.Apply(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	body, err := json.Marshal(map[string]string{"error": msg, "code": code})
	if err != nil {
		body = []byte(`{"error":"ingress error"}`)
	}
	_, _ = w.Write(body)
}
