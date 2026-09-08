// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package netpick

import "testing"

func TestConfiguredExclusions(t *testing.T) {
	t.Setenv("NVPAIR_EXCLUDED_ADDRESSES", "172.20.0.1, 192.0.2.0/24")
	got := Candidates([]string{"ip=192.0.2.10", "ips=172.20.0.1,198.51.100.2"}, []string{"172.20.0.1", "192.0.2.20"})
	if len(got) != 1 || got[0] != "198.51.100.2" {
		t.Fatalf("excluded addresses survived discovery: %v", got)
	}
	if got := rankLocal([]localIface{{name: "eth0", addrs: []localAddr{{ip: "192.0.2.10", prefixLen: 24}}}}, Evidence{}, ""); len(got) != 0 {
		t.Fatalf("excluded local address advertised: %v", got)
	}
	t.Setenv("NVPAIR_EXCLUDED_ADDRESSES", "")
	if len(Candidates(nil, []string{"172.20.0.1"})) != 1 {
		t.Fatal("empty override must disable fork exclusions")
	}
}
