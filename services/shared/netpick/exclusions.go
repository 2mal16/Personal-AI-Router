// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package netpick

import (
	"net/netip"
	"os"
	"strings"
)

// Fork default: exclude the overlapping container bridge address, not its subnet.
const defaultExcludedAddresses = "172.20.0.1"

func excludedAddress(address string) bool {
	value, configured := os.LookupEnv("NVPAIR_EXCLUDED_ADDRESSES")
	if !configured {
		value = defaultExcludedAddresses
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if prefix, err := netip.ParsePrefix(entry); err == nil && prefix.Contains(ip) {
			return true
		}
		if excluded, err := netip.ParseAddr(entry); err == nil && excluded == ip {
			return true
		}
	}
	return false
}
