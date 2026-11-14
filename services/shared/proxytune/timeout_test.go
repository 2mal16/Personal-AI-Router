// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package proxytune

import (
	"testing"
	"time"
)

func TestResponseHeaderTimeoutDefaultsWhenUnset(t *testing.T) {
	if got := ResponseHeaderTimeout(); got != defaultResponseHeaderTimeout {
		t.Fatalf("unset: got %v, want %v", got, defaultResponseHeaderTimeout)
	}
}

func TestResponseHeaderTimeoutHonoursConfiguredValue(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"10m", 10 * time.Minute},
		{"  90s  ", 90 * time.Second},
		{"500ms", 500 * time.Millisecond},
	} {
		t.Setenv("NVPAIR_RESPONSE_HEADER_TIMEOUT", tc.value)
		if got := ResponseHeaderTimeout(); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.value, got, tc.want)
		}
	}
}

// A bad setting must degrade to the default rather than disabling the timeout,
// which would let a wedged engine hold a request open indefinitely.
func TestResponseHeaderTimeoutFallsBackOnBadValues(t *testing.T) {
	for _, value := range []string{"", "banana", "0", "-30s", "120"} {
		t.Setenv("NVPAIR_RESPONSE_HEADER_TIMEOUT", value)
		if got := ResponseHeaderTimeout(); got != defaultResponseHeaderTimeout {
			t.Errorf("%q: got %v, want default %v", value, got, defaultResponseHeaderTimeout)
		}
	}
}
