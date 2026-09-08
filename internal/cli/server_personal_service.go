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

package cli

import "runtime"

// UnsupportedPersonalServiceError reports a personal Server service command
// that cannot use a native lifecycle manager. It has no caller-controlled
// fields so diagnostics cannot expose paths, endpoints, or credentials.
type UnsupportedPersonalServiceError struct{}

func (*UnsupportedPersonalServiceError) Error() string {
	if runtime.GOOS == "linux" {
		return "personal Server service lifecycle is not available in this build"
	}
	return "personal Server service is supported only on Linux"
}
