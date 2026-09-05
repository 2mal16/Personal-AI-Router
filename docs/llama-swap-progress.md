<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# llama-swap integration progress

Last updated: 2026-09-05. This is a working record, not a claim that the feature is ready.

## Goal and agreed scope

Add llama-swap to this fork as an externally managed engine. PAIR discovers its
models and routes inference across paired nodes. Existing llama-swap processes,
configuration, model loading, and installation remain under their existing
management. The user authorized implementation in this fork without upstream
maintainer alignment. Do not change or restart the user's running nodes while
implementing this feature.

Each hosting node connects to its own llama-swap on loopback, port 10000 by
default. The existing engine port setting will change PAIR's connection target,
not the server's listening port. Arbitrary remote URLs and API-key configuration
are outside the current implementation.

## Current implementation

- Added a manifest runtime mode, `external`, and bundled llama-swap manifest.
- External engine status probes `/v1/models`; inventory extracts `data[].id`.
- Start, stop, restart, install, and uninstall reject external engines. Shutdown
  skips them. Changing the configured port does not control the server process.
- Extended the existing OpenAI-compatible proxy (binary/wire name remains
  `lmstudio-proxy`) to hold both LM Studio and llama-swap local endpoints.
- Broker supplies their inventories. The shared `lm` discovery service denotes
  that proxy; model attribution distinguishes the engines. No worker was added.
- Inference selects a local endpoint by model, including at authenticated peer
  ingress. Model listing aggregates both endpoints. Duplicate IDs prefer LM Studio.
- Desktop engine types, capabilities, model controls, node-list presence, and
  the endpoints panel treat the engine as read-only and share one proxy row.
- Terminal interface labels the engine `(external)`, refuses its lifecycle keys,
  and keeps table navigation.

All changes are uncommitted. No live node configuration has been modified.

## Validation so far

- Existing `services/lmstudio-proxy` suite passed after the initial routing changes.
- Engine-manager suite started; result pending.
- Desktop typecheck cannot run yet because local dependencies are absent (`tsc`
  not found). Dependencies must be installed before validation.
- Go initially hit sandbox restrictions on its toolchain cache; rerun with
  approved access downloaded Go 1.25 and dependencies.

## Remaining work

1. Finish desktop presence, model attribution, and read-only external controls.
2. Finish terminal controls and expose connection-port configuration clearly.
3. Add regression coverage for external lifecycle isolation, port changes,
   inventory disappearance/recovery, simultaneous engines, and peer routing.
4. Review routing for loops, stale inventory, streaming, and engine attribution.
5. Update service/API/user documentation, regenerate service contracts, and bump
   every service binary whose compiled output changes.
6. Run relevant Go and desktop suites, lint, typecheck, contracts, SPDX, and
   dead-code checks. Record failures or skips explicitly.
7. Review the final diff and provide build/setup instructions for all three nodes.

## Known limits / unresolved checks

- Endpoint authentication and arbitrary remote URLs are not implemented.
- `/v1/models` reports available models, not necessarily resident models;
  loaded state must not be invented.
- Shared OpenAI proxy behavior must remain correct when both engines are present.
- Live three-node behavior has not been tested. No inference has been sent to the
  user's servers.

## Review checkpoint: upstream issue #24

Implementation paused at the user's request to review:

- https://github.com/NVIDIA/Personal-AI-Router/issues/24#issuecomment-5552115024
- https://github.com/jlacroix82/pair-multi-engine
- https://github.com/NVIDIA/Personal-AI-Router/pull/9

The commenter reports registering vLLM on stock PAIR 0.91.7 / engine-manager
0.17.4 using a dropped-in manifest, with remote lifecycle and model inventory
working over pinned mTLS. This matches the registry and inventory code already
inspected locally. Their README explicitly identifies missing desktop visibility
and closed routing as remaining gaps; it is not evidence that a manifest alone
provides the complete requested PAIR experience.

Their adapter supervises vLLM and implements model switching. llama-swap already
provides that function, so this project does not need another adapter or model
supervisor. Retain the existing manifest inventory machinery. The changes still
needed are explicit external lifecycle isolation and the routing/UI connection.
PR #9 reuses the OpenAI-compatible proxy, but also addresses independent engine
identity, manual-node identity collisions, and scheduling. Review those parts
before resuming implementation; current draft is not yet complete there.

The broad issue also proposes model aliases, capability/context filtering,
capacity policies, authentication headers, and per-endpoint timeouts. Those are
not automatically added to this task's scope. Cold-start inference timeouts do
need checking for llama-swap. The engine-action timeout discussed in the comment
is separate from the inference proxy's timeout.

Additional validation results available at this checkpoint:

- Desktop typecheck passed.
- Desktop unit tests passed: 37 files, 208 tests (before any new desktop tests).
- Engine-manager full suite failed in TestUninstallTerminatesRunningInstance and
  TestStopAllStopsDetachedCommandDaemonBeforeReadiness. PR #9 independently
  reports the first failure on pristine main/macOS; local baseline verification
  and diagnosis of the second are still needed.
- Broker full suite timed out after 600 seconds, with advertiser_test.go in the
  stack. Investigate the new advertiser RPC calls and fake-worker fixtures.
- Contract generation hit a sandbox restriction creating a tsx IPC pipe; rerun
  with the required access. Generated contracts are not yet verified.
- The latest proxy suite invocation was interrupted before a log appeared. Only
  the earlier initial proxy-suite pass is confirmed; new tests remain unverified.

## Implementation resumed after reference review

- Fixed the broker fixture hang: it read only the first of two endpoint updates.
  Broker suite now passes.
- Multi-engine proxy tests pass, covering separate model owners, aggregation,
  duplicate model preference, and exclusion after an endpoint becomes unhealthy.
- Added desktop tests for external capability restrictions and shared-proxy
  attribution. Desktop suite passes: 38 files / 210 tests.
- Scheduler now exposes llama-swap with the same node-wide ranking; suite passes.
- Typecheck passes; lint reports zero errors and one pre-existing formatting
  warning in untouched node-info-poller.ts.
- Contract generation succeeded with required IPC access.
- Verified TestUninstallTerminatesRunningInstance also fails on pristine HEAD on
  this macOS host. The latest full engine-manager run has only that failure; the
  detached-command shutdown test passed in both the baseline and latest run.
- Added setup documentation at docs/llama-swap.mdx and updated service READMEs.
- Added a peer-ingress streaming/pin-removal regression test; validation pending.
- Fixed external model-change pushes to update desktop inventory, and included
  per-engine attribution in proxy node-change comparison.

Remaining at that point: final proxy/TUI tests, final desktop checks after the
latest edits, SPDX/diff review, and any remaining failures.

## Test isolation correction and final review

A pre-existing broker test used Linux/Windows-only config isolation and wrote
`lmstudio-proxy-port.json` in the real macOS app directory. Its initial assertion
proved the prior effective value was the default 1234; the test wrote 1240.
With approval, restored 1234 and backed up the test-written file in
`/tmp/pair-test-written-proxy-port.json`. The test now exercises the reader with
an explicit temporary path. The proxy binary test now ignores persisted ports.
No remote node or llama-swap process was changed. Earlier statements that all
local configuration was untouched are superseded by this correction.

The peer-ingress streaming and pin-removal test passed; that proxy run failed
only because the existing binary test inherited the test-written port. Rerunning
after the isolation fix is pending. TUI tests passed before the additional
navigation regression test. Dead-code check passed with zero findings.

Version decisions: OpenAI proxy 1.0.0 because local-backend callers must now
provide model inventory for named inference; engine-manager 0.18.0, broker
0.41.0, scheduler 0.5.0, TUI 0.8.0 for additive external-engine support. Product,
installer, and desktop release numbers remain unchanged; no release is being cut.

## Completion pass

Work finished from the state above. Changes made in this pass:

- Fixed the new TUI regression test, which panicked instead of running: the
  bubbles table renders rows against its columns, so the view needs `SetSize`
  before `merge`. The engines suite now passes.
- Dropped the `localBackendTarget(models ...string)` wrapper in the proxy
  ingress; its one caller now uses `localEngineCandidates` directly.
- Self-candidate engine attribution now names the local endpoint that actually
  answers, not the preferred owner of the model ID. With LM Studio stopped, a
  model both engines list is attributed to llama-swap instead of LM Studio.
- A model-less request keeps both local engines as candidates, LM Studio first;
  the second one was previously ordered ahead of it and skipped host dedup.
- The ingress buffers a request body only on inference routes; everything else
  streams to the engine as before.
- The broker no longer converts an unqueryable inventory into an empty one.
  `engine:models` distinguishes "queried, no models" (key present, empty) from
  "not queryable" (key missing); `node/set-local-backend` now omits `models`
  in the second case, so a transient failure leaves the proxy's last-known
  inventory in place instead of refusing every named model until the next sweep.
- Desktop: the node-list engine strip rendered a start/stop switch and an
  install button for llama-swap, both of which the engine-manager rejects. It
  now reports Connected/Offline with no control. The endpoints panel listed the
  shared OpenAI proxy twice, once per engine; `mergeEndpointsByUrl` collapses it
  into one row named for both, and llama-swap alone still gets its own row.
- Desktop: the `llama-swap` proxy-port slot is no longer written (`getProxyPort`
  maps it onto the LM Studio entry), and its status re-emit now happens only on
  a real port change, like the engine it shares a listener with. `ProxyEngine`
  is an explicit `Extract` again rather than all of `EngineType`.
- Documentation: `docs/llama-swap.mdx` is now in the Fern navigation and the
  README reading order, `docs/engine-lifecycle.mdx` explains what "externally
  managed" excludes, and `docs/terminal-interface.mdx` says what the terminal
  interface will not do to such an engine and renames the shared proxy in the
  Proxies tab. The proxy and broker READMEs document the unknown/empty
  inventory distinction.

New coverage: bundled-manifest shape and read-only action rules
(engine-manager), self-candidate attribution and ordering (proxy), and the
unknown-inventory wire behavior (broker), plus the endpoint merge (desktop).

## Final validation

Run on macOS/arm64 with Go 1.25.0 and Node 22.

- Go suites pass in every module except two failures that are not this change:
  - `nvpair-engine-manager` `TestUninstallTerminatesRunningInstance`, verified
    failing the same way in a pristine `HEAD` worktree on this host.
  - `ollama-proxy` `TestAliasSelfTargetMatchesBoundLoopbackAddressNotPortAlone`,
    which cannot bind `127.0.0.2` on this host. `ollama-proxy` is untouched here.
- `services/tests` (cross-process): one failure,
  `TestWorkloadManagerOutboundBroadcast`. Its workload-manager could not bind
  `:14320` because the installed PAIR application was running and holding that
  port, so its restart budget ran out and no broadcast was ever sent. The
  sibling tests in `cluster_data_plane_test.go` skip when `portBusy(14320)`;
  this one has no such guard. Not caused by this change, and not fixed here.
  A rerun on a host not running PAIR is still owed.
- Desktop: typecheck, 38 files / 211 unit tests, lint (zero errors; the one
  pre-existing prettier warning in untouched `node-info-poller.ts` remains),
  `dead-code:check`, and `service-contracts:check` all pass.
- `node scripts/spdx-headers.mjs`: 876 files checked, 0 missing.

Running the cross-process suite started real service binaries on this machine:
they bound loopback ports and advertised over mDNS for the duration of the run,
then shut down. No node configuration was written and the installed PAIR
application kept running throughout. Still untested: live three-node behavior,
and no inference has been sent to the user's servers.
