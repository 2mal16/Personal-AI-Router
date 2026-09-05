// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it, vi } from 'vitest'

vi.mock('electron', () => ({ BrowserWindow: { getAllWindows: () => [] } }))
vi.mock('@/electron/window', () => ({ createOverviewWindow: vi.fn() }))

import { getModularBridgeState } from '@/electron/service-bridge/modular-state'
import { EngineCapabilities } from '@/ui/constants/engine-capabilities'
import { getWelcomeEngineCandidates } from '@/ui/constants/welcome'
import { mergeEndpointsByUrl } from '@/ui/utils/gateway-inference-paths'

describe('externally managed llama-swap', () => {
    it('shows the engine and its shared proxy without inferring LM Studio presence', () => {
        const state = getModularBridgeState()
        state.setSelfId('swap-local')
        state.handleNotification({
            source: 'lmstudio-proxy',
            method: 'ready',
            params: { port: 1234 }
        })
        state.applyEngineManagerStatus({
            engine: 'llama-swap',
            installed: true,
            running: true,
            healthy: true,
            port: 10000,
            externally_managed: true
        })
        expect(
            state
                .getEngineInitialState()
                .statuses.find(s => s.nodeId === 'swap-local' && s.engineType === 'llama-swap')
        ).toMatchObject({ processStatus: 'running', enginePort: 10000, proxyPort: 1234 })
        state.handleNotification({
            source: 'lmstudio-proxy',
            method: 'node/discovered',
            params: {
                id: 'swap-peer',
                host: 'swap-peer',
                port: 1234,
                addresses: ['192.0.2.10'],
                ip: '192.0.2.10',
                modelsByEngine: { 'llama-swap': ['swap-model'] }
            }
        })
        expect(state.isRemoteEngineRunning('swap-peer', 'llama-swap')).toBe(true)
        expect(state.isRemoteEngineRunning('swap-peer', 'lm-studio')).toBe(false)
        state.handleNotification({
            source: 'lmstudio-proxy',
            method: 'node/removed',
            params: { id: 'swap-peer' }
        })
        expect(state.isRemoteEngineRunning('swap-peer', 'llama-swap')).toBe(false)
    })

    it('offers no installation or destructive model operations', () => {
        expect(EngineCapabilities['llama-swap']).toMatchObject({
            externallyManaged: true,
            hasInstall: [],
            hasEject: false,
            hasDeleteModel: false
        })
        expect(getWelcomeEngineCandidates('Linux')).not.toContain('llama-swap')
    })

    it('lists the shared OpenAI endpoint once, named for both engines', () => {
        expect(
            mergeEndpointsByUrl([
                { name: 'Ollama', url: 'http://127.0.0.1:11435' },
                { name: 'LM Studio', url: 'http://127.0.0.1:1234' },
                { name: 'llama-swap', url: 'http://127.0.0.1:1234' }
            ])
        ).toEqual([
            { name: 'Ollama', url: 'http://127.0.0.1:11435' },
            { name: 'LM Studio / llama-swap', url: 'http://127.0.0.1:1234' }
        ])
        // llama-swap alone still gets its own row: the LM Studio one is absent
        // when that engine is not running cluster-wide.
        expect(mergeEndpointsByUrl([{ name: 'llama-swap', url: 'http://127.0.0.1:1234' }])).toEqual(
            [{ name: 'llama-swap', url: 'http://127.0.0.1:1234' }]
        )
    })
})
