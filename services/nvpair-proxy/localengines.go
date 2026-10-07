// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// Several local engines behind one facade.
//
// An engine that speaks a facade's dialect can ride on it rather than get a
// facade and a port of its own: llama-swap, an externally managed
// OpenAI-compatible server, is fronted by the lmstudio facade (see
// engineProfile.LocalEngines). Everything specific to that lives in this file;
// the rest of the proxy only calls these hooks. A facade whose profile lists no
// extra engine fronts just its own and behaves exactly as before.

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"

	"nvpair-shared/noderec"
)

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

// localEngineNames returns the engines this facade fronts locally, preferred
// first. It is never empty: a profile that lists none fronts its own engine.
func (p engineProfile) localEngineNames() []string {
	if len(p.LocalEngines) == 0 {
		return []string{p.Name}
	}
	return p.LocalEngines
}

// nodeModels is the model inventory a peer advertises for this facade: its own
// engine's models plus those of every other engine the facade fronts, so a
// model only llama-swap serves still makes the node an eligible owner.
func (p engineProfile) nodeModels(n noderec.DirectoryNode) []string {
	models := append([]string(nil), n.EngineModels(p.Name)...)
	for _, name := range p.localEngineNames() {
		if name != p.Name {
			models = append(models, n.ModelsByEngine[name]...)
		}
	}
	return models
}

// engineName is the engine to attribute work sent to this candidate to,
// falling back to the facade's own when the candidate does not name one.
func (c candidate) engineName(fallback string) string {
	if c.engine != "" {
		return c.engine
	}
	return fallback
}

// engineForModel names which of engines advertises model on this node, for
// attributing work to the engine that serves it. The first engine in the list
// wins a duplicate id, matching local routing preference; with no model, or one
// no inventory lists, it falls back to the first engine.
func (n Node) engineForModel(engines []string, model string) string {
	for _, name := range engines {
		if slices.Contains(n.ModelsByEngine[name], model) {
			return name
		}
	}
	return engines[0]
}
