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

package source

// ResourceNotFoundError contains no protected Source or Scope identity.
type ResourceNotFoundError struct{}

func (*ResourceNotFoundError) Error() string { return "Source resource was not found" }

// ResourceConflictError reports an immutable identity collision without
// retaining either the existing or rejected Source content or identity.
type ResourceConflictError struct{}

func (*ResourceConflictError) Error() string { return "Source resource identity conflicts" }
