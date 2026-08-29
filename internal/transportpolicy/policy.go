// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package transportpolicy defines the shared network transport trust rules
// used by PowerContext process and client surfaces.
package transportpolicy

import (
	"net/netip"
	"net/url"
	"strings"
)

// IsLoopbackHost reports whether host is localhost, IPv6 loopback, or an IPv4
// literal in the complete 127.0.0.0/8 loopback range.
func IsLoopbackHost(host string) bool {
	normalized := strings.TrimSpace(host)
	if unbracketed, ok := strings.CutPrefix(normalized, "["); ok {
		var closed bool
		normalized, closed = strings.CutSuffix(unbracketed, "]")
		if !closed {
			return false
		}
	}
	if strings.EqualFold(normalized, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(normalized)
	return err == nil && address.IsLoopback()
}

// IsPlaintextNonLoopback reports whether endpoint uses plaintext HTTP for a
// host outside the loopback ranges trusted by PowerContext.
func IsPlaintextNonLoopback(endpoint *url.URL) bool {
	return endpoint != nil && endpoint.Scheme == "http" && !IsLoopbackHost(endpoint.Hostname())
}
