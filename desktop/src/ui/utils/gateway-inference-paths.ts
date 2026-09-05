// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

import { EngineType } from '@/shared/types/engines'

/** Base URL shown for a local inference endpoint. */
export function gatewayEndpointDisplayUrl(
    proxyPort: number,
    inferenceType: EngineType
): string | null {
    void inferenceType
    if (proxyPort <= 0) return null
    return `http://127.0.0.1:${proxyPort}`
}

/**
 * Collapse endpoints that resolve to the same URL into one row named for every
 * engine behind it. One OpenAI-compatible proxy fronts both LM Studio and
 * llama-swap, so both engines report its port; listing that twice reads as two
 * endpoints to choose between when there is only one to point a client at.
 * Order follows the first appearance of each URL.
 */
export function mergeEndpointsByUrl<T extends { name: string; url: string | null }>(
    endpoints: T[]
): T[] {
    const byUrl = new Map<string, T>()
    for (const endpoint of endpoints) {
        const key = endpoint.url ?? endpoint.name
        const existing = byUrl.get(key)
        if (!existing) {
            byUrl.set(key, endpoint)
            continue
        }
        byUrl.set(key, { ...existing, name: `${existing.name} / ${endpoint.name}` })
    }
    return [...byUrl.values()]
}
