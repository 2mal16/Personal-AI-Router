// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { EngineManagerNames } from '@/shared/constants/engines'
import type { EngineType } from '@/shared/types/engines'
import { engineTypeFromManagerName } from '@/shared/utils/engines'

/**
 * Engines currently consumed through the broker's proxy plane. Keep this set
 * separate from the complete engine registry so an engine can be declared
 * before every proxy-state consumer is ready to handle it.
 */
export type ProxyEngine = Extract<EngineType, 'ollama' | 'lm-studio' | 'llama-cpp' | 'vllm'>
/** A proxy's source name: the engine-manager's name for the engine, plus -proxy. */
export type ProxyNodeSource = `${(typeof EngineManagerNames)[ProxyEngine]}-proxy`

const PROXY_SOURCE_SUFFIX = '-proxy'

export const PROXY_ENGINES: readonly ProxyEngine[] = ['ollama', 'lm-studio', 'llama-cpp', 'vllm']

export const PROXY_NODE_SOURCES: readonly ProxyNodeSource[] =
    PROXY_ENGINES.map(proxySourceForEngine)

export function isProxyEngine(engine: EngineType): engine is ProxyEngine {
    return PROXY_ENGINES.some(candidate => candidate === engine)
}

export function proxySourceForEngine(engine: ProxyEngine): ProxyNodeSource {
    return `${EngineManagerNames[engine]}${PROXY_SOURCE_SUFFIX}`
}

export function proxyEngineFromSource(source: ProxyNodeSource): ProxyEngine
export function proxyEngineFromSource(source: string): ProxyEngine | null
export function proxyEngineFromSource(source: string): ProxyEngine | null {
    if (!source.endsWith(PROXY_SOURCE_SUFFIX)) return null
    return proxyEngineFromManagerId(source.slice(0, -PROXY_SOURCE_SUFFIX.length))
}

export function proxyEngineFromManagerId(managerName: string): ProxyEngine | null {
    const engine = engineTypeFromManagerName(managerName)
    return engine !== null && isProxyEngine(engine) ? engine : null
}

/**
 * Engines with no proxy of their own, each served by another engine's proxy.
 * llama-swap is externally managed and OpenAI-compatible, so it rides LM
 * Studio's facade: its models are listed and routed there, and its presence is
 * read from that facade's per-engine inventory.
 */
export type HostedEngine = Extract<EngineType, 'llama-swap'>

const HOSTED_ENGINE_FACADE: Record<HostedEngine, ProxyEngine> = { 'llama-swap': 'lm-studio' }

/** Every engine whose presence the proxy plane reports, with or without a proxy of its own. */
export type RoutedEngine = ProxyEngine | HostedEngine

export const ROUTED_ENGINES: readonly RoutedEngine[] = [...PROXY_ENGINES, 'llama-swap']

function isHostedEngine(engine: EngineType): engine is HostedEngine {
    return Object.hasOwn(HOSTED_ENGINE_FACADE, engine)
}

export function isRoutedEngine(engine: EngineType): engine is RoutedEngine {
    return isProxyEngine(engine) || isHostedEngine(engine)
}

/** The proxy that fronts an engine: its own, or the one hosting it. */
export function facadeForEngine(engine: RoutedEngine): ProxyEngine {
    return isHostedEngine(engine) ? HOSTED_ENGINE_FACADE[engine] : engine
}

/** The engines a proxy fronts: its own first, then any it hosts. */
export function enginesOnFacade(facade: ProxyEngine): RoutedEngine[] {
    const hosted = ROUTED_ENGINES.filter(
        engine => isHostedEngine(engine) && HOSTED_ENGINE_FACADE[engine] === facade
    )
    return [facade, ...hosted]
}
