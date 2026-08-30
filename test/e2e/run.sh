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
    sqlite | oceanbase) ;;
    *)
        echo "POWERCONTEXT_E2E_DATABASE must be sqlite or oceanbase" >&2
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

run_id=${GITHUB_RUN_ID:-local}
run_attempt=${GITHUB_RUN_ATTEMPT:-1}
container="powercontext-e2e-oceanbase-${run_id}-${run_attempt}"

stop_oceanbase() {
    docker rm --force "$container" >/dev/null 2>&1 || true
}

if [ "$command" = down ]; then
    if [ "$database" = oceanbase ]; then
        stop_oceanbase
    fi
    exit
fi

if [ "$command" = check ]; then
    CGO_ENABLED=1 go test -run '^$' -tags sqlite_fts5 ./test/e2e
    if [ "$database" = oceanbase ]; then
        docker version >/dev/null
    fi
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

if [ "$database" = sqlite ]; then
    CGO_ENABLED=1 go test -count=1 -tags sqlite_fts5 -json ./test/e2e > "$output/go-test.jsonl"
    make build VERSION=ci COMMIT="$source_sha" BUILD_DATE=1970-01-01T00:00:00Z
    go run ./tools/process-smoke -binary bin/powercontext -env-file .env.example -version ci > "$output/process-smoke.log" 2>&1
    exit
fi

cleanup() {
    status=$?
    trap - EXIT INT TERM
    stop_oceanbase
    exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
image=${POWERCONTEXT_E2E_OCEANBASE_IMAGE:-ghcr.io/oceanbase/oceanbase-ce@sha256:31086a6900c21c479c2bcd942b6a28c53b17a51f4e9b9eb8eafcc596adfcd2e3}
docker run --detach \
    --name "$container" \
    --ulimit nofile=65536:65536 \
    --env MODE=slim \
    --env OB_DATABASE=powercontext \
    --env OB_TENANT_PASSWORD=powercontext-e2e \
    --publish 127.0.0.1::2881 \
    "$image" >/dev/null

attempt=1
while ! docker exec "$container" \
    obclient -h127.0.0.1 -P2881 -uroot@test -ppowercontext-e2e \
    -Dpowercontext -e 'SELECT 1' >/dev/null 2>&1; do
    if [ "$attempt" -ge 120 ]; then
        docker logs "$container" >&2
        echo "OceanBase did not become ready" >&2
        exit 1
    fi
    attempt=$((attempt + 1))
    sleep 5
done

endpoint=$(docker port "$container" 2881/tcp | head -n 1)
port=${endpoint##*:}
POWERCONTEXT_TEST_OCEANBASE_URL="mysql+aoceanbase://root%40test:powercontext-e2e@127.0.0.1:${port}/powercontext?charset=utf8mb4" \
    go test -count=1 -run '^TestLiveOceanBaseProfileSmoke$' -json ./test/e2e > "$output/go-test.jsonl"
