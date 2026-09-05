// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
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
// clears) the named local engine this facade's ingress serves.
//
// A non-loopback host is rejected rather than stored. The ingress forwards a
// pin-authenticated peer's request straight here without consulting discovery,
// so an off-box host would turn this node into a relay to an address chosen by
// whoever can reach the control channel. The broker only ever sends 127.0.0.1;
// this is the same defence-in-depth re-validation setLoopbackAlias performs on
// the alias the broker sends it.
//
// An empty b.Engine means the facade's own engine. A name the profile does not
// host is rejected, so one facade can never be handed another's backend.
func (f *facade) setLocalBackend(b localBackend) error {
	if b.Host != "" && !isLoopbackHost(b.Host) {
		return fmt.Errorf("local backend host %q is not loopback", b.Host)
	}
	if b.Engine == "" {
		b.Engine = f.profile.Name
	}
	if !slices.Contains(f.profile.localEngineNames(), b.Engine) {
		return fmt.Errorf("local backend engine %q is not served by the %s facade", b.Engine, f.profile.Name)
	}
	f.backendMu.Lock()
	defer f.backendMu.Unlock()
	if f.localEngines == nil {
		f.localEngines = make(map[string]localBackend)
	}
	// Port/liveness-only updates must not erase a known model inventory. Only a
	// facade that hosts several engines keeps one: with a single engine every
	// request goes to it, and an empty inventory must not read as "serves
	// nothing".
	if b.Models == nil && len(f.profile.localEngineNames()) > 1 {
		if previous, ok := f.localEngines[b.Engine]; ok && previous.Port == b.Port {
			b.Models = previous.Models
		} else {
			b.Models = []string{}
		}
	}
	f.localEngines[b.Engine] = b
	return nil
}

// localEngineCandidates returns the loopback engines this machine has
// configured for this facade, in the profile's preference order, restricted to
// the ones whose inventory advertises model when one is named: LM Studio wins a
// duplicate model ID and llama-swap stays independently routable. An engine
// that is unset, has no port, or is unhealthy is not a candidate, so an empty
// result is what makes the ingress answer 503 rather than forward. The host
// defaults to 127.0.0.1, and setLocalBackend refuses to store anything that is
// not loopback, so every target here is loopback.
func (f *facade) localEngineCandidates(model string) []candidate {
	f.backendMu.RLock()
	defer f.backendMu.RUnlock()
	var out []candidate
	names := f.profile.localEngineNames()
	multi := len(names) > 1
	for _, engine := range names {
		b, ok := f.localEngines[engine]
		if !ok || b.Port <= 0 || !b.Healthy {
			continue
		}
		if multi && model != "" && b.Models != nil && !slices.Contains(b.Models, model) {
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
func (f *facade) handlePlain(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRemote(r.RemoteAddr) {
		slog.Warn("rejected non-loopback plaintext request; cluster peers must use mTLS",
			"remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path)
		writeIngressError(w, http.StatusForbidden, "loopback-only",
			"plaintext requests are accepted only from loopback; cluster peers must use the mTLS ingress")
		return
	}
	// Engine-manager marks its private identity/action requests so this
	// compatibility facade can never be mistaken for the local Ollama backend.
	if r.Header.Get(engineIdentityProbeHeader) == "1" {
		writeIngressError(w, http.StatusConflict, "proxy-facade",
			"the compatibility facade is not the "+f.profile.DisplayName+" engine")
		return
	}
	f.handleHTTP(w, r)
}

// handleClusterIngress is the LAN mTLS personality: it authenticates the caller
// against this node's cluster pins and, once the peer is a trusted cluster
// member, forwards the request straight to the local loopback engine — exactly
// like the local plaintext path, with no route filtering. The mTLS pin is the
// sole authorization boundary (a trusted peer is treated like a local client),
// so the two personalities stay behaviorally identical toward the engine. It
// never calls resolveCandidates, so a peer request cannot be re-routed onward.
func (f *facade) handleClusterIngress(w http.ResponseWriter, r *http.Request) {
	// Re-derive membership and pins per request so a cluster left, or a peer
	// paired or removed, after startup is reflected immediately without a proxy
	// restart — a removed peer must stop being accepted right away, which is the
	// whole point of the gate.
	f.host.mesh.Refresh()
	peer, ok := f.host.mesh.VerifyClientPin(r)
	if !ok {
		writeIngressError(w, http.StatusForbidden, "cluster-auth",
			"client certificate is not a pinned member of this node's cluster")
		return
	}
	// A facade hosting several local engines answers a model list from all of
	// them; a single-engine facade forwards it untouched, as it always has.
	if r.Method == http.MethodGet && len(f.profile.localEngineNames()) > 1 {
		if role, ok := f.profile.roleFor(r.Method, r.URL.Path); ok && role.isModelList() {
			_, _ = f.serveModelList(w, r, role, f.localEngineCandidates(""))
			return
		}
	}
	// Only an inference route needs its model, and reading it means buffering
	// the body. Everything else streams to the engine as it always has.
	model := ""
	if isInferenceRequest(f.profile, r.Method, r.URL.Path) {
		body, parsed := bufferBodyAndModel(r)
		r.Body = io.NopCloser(bytes.NewReader(body))
		model = f.profile.normalizeModel(parsed)
	}
	candidates := f.localEngineCandidates(model)
	if len(candidates) == 0 {
		writeIngressError(w, http.StatusServiceUnavailable, "no-local-backend",
			"no local inference backend is available on this node")
		return
	}
	target := candidates[0].url
	slog.Debug("cluster ingress forwarding to local backend",
		"peer", peer, "method", r.Method, "path", r.URL.Path, "target", target.Host)
	f.reverseProxyToLocal(w, r, target)
}

// reverseProxyToLocal streams the request to the local engine, preserving
// cancellation (the request context is the proxy's root context, so a client
// disconnect or shutdown tears down the upstream call and stops generation).
func (f *facade) reverseProxyToLocal(w http.ResponseWriter, r *http.Request, target *url.URL) {
	f.newLocalReverseProxy(target).ServeHTTP(w, r)
}

func (f *facade) newLocalReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		Transport: f.host.plainHTTPTransport(),
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

// writeIngressError returns the actual failure without granting browser permissions.
func writeIngressError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	body, err := json.Marshal(map[string]string{"error": msg, "code": code})
	if err != nil {
		body = []byte(`{"error":"ingress error"}`)
	}
	_, _ = w.Write(body)
}
