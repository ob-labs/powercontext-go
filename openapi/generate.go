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

// Package openapi owns generation of the immutable HTTP contract.
package openapi

//go:generate go run ../tools/api-generate -spec powercontext.yaml -target ../api/v1 -package v1 -client-invoker ../client/invoker_gen.go -compatibility compatibility-surface.json
//go:generate go run ../tools/api-generate -spec canonical/upstream-powercontext.yaml -scope-sidecar-manifest canonical/scopes-manifest.json -target ../api/canonical/scopes -package scopes -compatibility compatibility-surface.json -legacy-spec powercontext.yaml
//go:generate go run ../tools/api-generate -spec canonical/upstream-powercontext.yaml -source-sidecar-manifest canonical/sources-manifest.json -target ../api/canonical/sources -package sources -compatibility compatibility-surface.json -legacy-spec powercontext.yaml
//go:generate go run ../tools/api-generate -spec canonical/upstream-powercontext.yaml -artifact-sidecar-manifest canonical/artifacts-manifest.json -target ../api/canonical/artifacts -package artifacts -compatibility compatibility-surface.json -legacy-spec powercontext.yaml
//go:generate go run ../tools/api-generate -spec canonical/upstream-powercontext.yaml -managed-skill-sidecar-manifest canonical/managed-skills-manifest.json -target ../api/canonical/managedskills -package managedskills -compatibility compatibility-surface.json -legacy-spec powercontext.yaml
//go:generate go run ../tools/api-generate -spec canonical/upstream-powercontext.yaml -stats-sidecar-manifest canonical/stats-manifest.json -target ../api/canonical/stats -package stats -compatibility compatibility-surface.json -legacy-spec powercontext.yaml
