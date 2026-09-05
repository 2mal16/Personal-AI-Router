// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExternalEngineInventoryAndLifecycleIsolation(t *testing.T) {
	var available atomic.Bool
	available.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if !available.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"chat-model"},{"id":"embedding-model"}]}`))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	reg := NewRegistry()
	mf := &Manifest{Engine: "llama-swap", DisplayName: "llama-swap", ManifestVersion: 1,
		Platforms: map[string]Platform{runtime.GOOS + "/" + runtime.GOARCH: {Runtime: Runtime{Mode: "external", Port: port, Ready: &Probe{HTTP: "http://127.0.0.1:{port}/v1/models"}}}},
		Actions:   map[string]Action{"list_models": {HTTP: &ActionHTTP{Method: "GET", Path: "/v1/models"}, Result: &ActionResult{Array: "data", Field: "id"}}},
	}
	if err := mf.Validate(); err != nil {
		t.Fatal(err)
	}
	reg.engines[mf.Engine] = mf
	ex := NewExecutor(reg, NewReporter(nil), func(string, any) {}, t.TempDir())
	ex.overrideDir = t.TempDir()
	ctx := context.Background()
	status, err := ex.Status(mf.Engine)
	if err != nil || !status.Running || !status.ExternallyManaged {
		t.Fatalf("status = %+v, %v", status, err)
	}
	inventory := ex.ModelsResult(ctx)
	if len(inventory.ByEngine[mf.Engine]) != 2 || len(inventory.LoadedByEngine) != 0 {
		t.Fatalf("inventory = %+v", inventory)
	}
	for name, op := range map[string]func() error{
		"start": func() error { return ex.Start(ctx, mf.Engine) }, "stop": func() error { return ex.Stop(mf.Engine) },
		"restart": func() error { return ex.Restart(ctx, mf.Engine) }, "install": func() error { return ex.Install(ctx, mf.Engine) },
		"uninstall": func() error { return ex.Uninstall(ctx, mf.Engine) },
	} {
		if err := op(); err == nil {
			t.Errorf("%s must reject external lifecycle", name)
		}
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer other.Close()
	otherURL, _ := url.Parse(other.URL)
	otherPort, _ := strconv.Atoi(otherURL.Port())
	if _, err := ex.SetPort(ctx, mf.Engine, otherPort); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.SetPort(ctx, mf.Engine, port); err != nil {
		t.Fatal(err)
	}
	available.Store(false)
	if got := ex.ModelsResult(ctx); len(got.Models) != 0 {
		t.Fatalf("unavailable inventory = %+v", got)
	}
	available.Store(true)
	if got := ex.ModelsResult(ctx); len(got.Models) != 2 {
		t.Fatalf("recovered inventory = %+v", got)
	}
	if err := ex.setDesiredEnabled(mf.Engine, true); err != nil {
		t.Fatal(err)
	}
	if err := ex.RestoreEnabled(ctx); err != nil {
		t.Fatal(err)
	}
	ex.StopAll()
	response, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("shutdown affected external server")
	}
}

func TestExternalManifestRejectsLifecycleCommands(t *testing.T) {
	base := Platform{Runtime: Runtime{Mode: "external", Port: 10000, Ready: &Probe{HTTP: "http://127.0.0.1:{port}/v1/models"}}}
	for _, change := range []func(*Platform){
		func(p *Platform) { p.Runtime.Bin = "engine" },
		func(p *Platform) { p.Runtime.Start = [][]string{{"engine", "serve"}} },
		func(p *Platform) { p.Runtime.Stop = &StopSpec{Cmd: []string{"engine", "stop"}} },
		func(p *Platform) { p.Install = &Install{} },
		func(p *Platform) { p.Runtime.Port = 0 },
		func(p *Platform) { p.Runtime.Ready = nil },
	} {
		p := base
		change(&p)
		if err := p.validate("linux/amd64"); err == nil {
			t.Fatalf("accepted invalid external platform: %+v", p)
		}
	}
}

// TestBundledLlamaSwapManifestIsExternalAndReadOnly pins the shipped manifest:
// it must load from the embedded set on every platform PAIR builds for, probe
// the documented loopback port, and expose nothing but a read-only inventory
// action. A lifecycle command or a write action added here would make the
// manager control a server it does not own.
func TestBundledLlamaSwapManifestIsExternalAndReadOnly(t *testing.T) {
	reg := NewRegistry()
	if err := reg.LoadFS(bundledManifests, "manifests"); err != nil {
		t.Fatal(err)
	}
	m, ok := reg.Get("llama-swap")
	if !ok {
		t.Fatal("llama-swap manifest not loaded")
	}
	for _, key := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		p, ok := m.Platforms[key]
		if !ok {
			t.Errorf("%s: platform missing", key)
			continue
		}
		if p.Runtime.modeOrDefault() != "external" {
			t.Errorf("%s: runtime.mode = %q, want external", key, p.Runtime.Mode)
		}
		if p.Runtime.Port != 10000 {
			t.Errorf("%s: runtime.port = %d, want 10000", key, p.Runtime.Port)
		}
		if p.Runtime.Ready == nil || !strings.Contains(p.Runtime.Ready.HTTP, "/v1/models") {
			t.Errorf("%s: readiness probe = %+v, want a /v1/models GET", key, p.Runtime.Ready)
		}
	}
	if len(m.Actions) != 1 {
		t.Fatalf("actions = %+v, want list_models alone", m.Actions)
	}
	action, ok := m.Actions["list_models"]
	if !ok || action.HTTP == nil || action.HTTP.Method != http.MethodGet {
		t.Fatalf("list_models = %+v, want an HTTP GET", action)
	}
	if action.Result == nil || action.Result.Array != "data" || action.Result.Field != "id" {
		t.Fatalf("list_models result = %+v, want data[].id", action.Result)
	}
}

// TestExternalManifestRejectsWriteActions covers the other half of the external
// contract: an action that could change the server — a command, a file removal,
// a non-GET call, or one that wants a restart afterwards — is refused at load.
func TestExternalManifestRejectsWriteActions(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{Engine: "external-engine", DisplayName: "External", ManifestVersion: 1,
			Platforms: map[string]Platform{"linux/amd64": {Runtime: Runtime{Mode: "external", Port: 10000, Ready: &Probe{HTTP: "http://127.0.0.1:{port}/v1/models"}}}},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid external manifest rejected: %v", err)
	}
	for name, action := range map[string]Action{
		"delete_model": {HTTP: &ActionHTTP{Method: http.MethodDelete, Path: "/v1/models/{model}"}},
		"load_model":   {HTTP: &ActionHTTP{Method: http.MethodPost, Path: "/v1/load"}},
		"pull_model":   {Cmd: []string{"llama-swap", "pull", "{model}"}},
		"reload":       {HTTP: &ActionHTTP{Method: http.MethodGet, Path: "/reload"}, RestartAfter: true},
		"remove_cache": {RemovePath: &ActionRemovePath{}},
		"list_untyped": {},
	} {
		m := base()
		m.Actions = map[string]Action{name: action}
		if err := m.Validate(); err == nil {
			t.Errorf("%s: accepted a write action on an external engine", name)
		}
	}
}
