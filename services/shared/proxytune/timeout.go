// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package proxytune

import (
	"os"
	"strings"
	"time"
)

// Default: an engine that has not sent response headers within two minutes is
// treated as unresponsive and the request fails over to the next candidate.
const defaultResponseHeaderTimeout = 120 * time.Second

// ResponseHeaderTimeout bounds how long a proxy waits for an upstream engine to
// return response headers before giving up on that candidate.
//
// The default suits an engine that is already resident: it answers in
// milliseconds, and silence that long means it is wedged. It is too short for
// an engine that loads a model on demand, because nothing — not even headers —
// is sent while the model loads. vLLM in particular spends that time on weight
// loading, torch.compile and CUDA graph capture, so a cold start can exceed two
// minutes and the first request fails even though the load later succeeds.
// Raise this on nodes whose engines cold-start large models.
//
// An unset, unparsable or non-positive value yields the default, so a bad
// setting degrades to current behaviour rather than disabling the timeout.
func ResponseHeaderTimeout() time.Duration {
	value, configured := os.LookupEnv("NVPAIR_RESPONSE_HEADER_TIMEOUT")
	if !configured {
		return defaultResponseHeaderTimeout
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return defaultResponseHeaderTimeout
	}
	return parsed
}
