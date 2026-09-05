#!/bin/sh
# Copyright (c) 2026 OceanBase.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
cd "$root"

command=${1:-acceptance}
if [ "$#" -gt 0 ]; then
    shift
fi
test "$#" -eq 0 || { echo "$command does not accept arguments" >&2; exit 2; }

database=${POWERCONTEXT_E2E_DATABASE:-sqlite}
case "$database" in
    sqlite) ;;
    *)
        echo "POWERCONTEXT_E2E_DATABASE must be sqlite" >&2
        exit 2
        ;;
esac

case "$command" in
    acceptance | check | down) ;;
    *)
        echo "command must be acceptance, check, or down" >&2
        exit 2
        ;;
esac

if [ "$command" = down ]; then
    exit
fi

if [ "$command" = check ]; then
    CGO_ENABLED=1 go test -run '^$' -tags sqlite_fts5 ./test/e2e
    exit
fi

output=${POWERCONTEXT_E2E_OUTPUT:-"$root/.powercontext-e2e/$database/acceptance"}
mkdir -p "$output"
output=$(CDPATH= cd -- "$output" && pwd)
source_sha=${GITHUB_SHA:-$(git rev-parse HEAD)}
test "${#source_sha}" -eq 40 || { echo "GITHUB_SHA must be a full commit SHA" >&2; exit 2; }
case "$source_sha" in
    *[!0-9a-f]*) echo "GITHUB_SHA must be lowercase hexadecimal" >&2; exit 2 ;;
esac
printf '{"database":"%s","source_sha":"%s"}\n' "$database" "$source_sha" > "$output/run.json"

CGO_ENABLED=1 go test -count=1 -tags sqlite_fts5 -json ./test/e2e > "$output/go-test.jsonl"
make build VERSION=ci COMMIT="$source_sha" BUILD_DATE=1970-01-01T00:00:00Z
go run ./tools/process-smoke -binary bin/powercontext -env-file .env.example -version ci > "$output/process-smoke.log" 2>&1
