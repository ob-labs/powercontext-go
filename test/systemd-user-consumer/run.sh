#!/usr/bin/env bash
#
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

# Run the archive consumer under a disposable, non-root systemd --user manager.
set -euo pipefail

usage() {
  echo "usage: run.sh --archive ABSOLUTE_PATH --test-binary ABSOLUTE_PATH" >&2
  exit 2
}

archive=""
test_binary=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --archive)
      [ "$#" -ge 2 ] || usage
      archive="$2"
      shift 2
      ;;
    --test-binary)
      [ "$#" -ge 2 ] || usage
      test_binary="$2"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

if [ "$(id -u)" -eq 0 ]; then
  echo "systemd user consumer must not run as root" >&2
  exit 2
fi

require_absolute_regular_file() {
  local value="$1"
  case "$value" in
    /*) ;;
    *) echo "systemd user consumer input must be absolute" >&2; exit 2 ;;
  esac
  if [ -L "$value" ] || ! [ -f "$value" ]; then
    echo "systemd user consumer input must be a regular file" >&2
    exit 2
  fi
}

require_absolute_regular_file "$archive"
require_absolute_regular_file "$test_binary"
archive="$(realpath -- "$archive")"
test_binary="$(realpath -- "$test_binary")"

runner_temp="${RUNNER_TEMP:-}"
case "$runner_temp" in
  /*) ;;
  *) echo "RUNNER_TEMP must be an absolute directory" >&2; exit 2 ;;
esac
if ! [ -d "$runner_temp" ]; then
  echo "RUNNER_TEMP must name an existing directory" >&2
  exit 2
fi
runner_temp="$(realpath -- "$runner_temp")"

workspace="$(mktemp -d "$runner_temp/powercontext-systemd-user.XXXXXX")"
diagnostics="$(mktemp -d "$runner_temp/powercontext-systemd-user-consumer.XXXXXX")"
chmod 0700 "$workspace" "$diagnostics"
home="$workspace/home"
runtime="$workspace/runtime"
temporary="$workspace/tmp"
mkdir -m 0700 "$home" "$runtime" "$temporary"

write_summary() {
  local result="$1"
  local manager_ready=false
  if [ -f "$workspace/manager-ready" ]; then
    manager_ready=true
  fi
  local systemd_version=unknown
  if command -v systemd >/dev/null 2>&1; then
    systemd_version="$(systemd --version 2>/dev/null | awk 'NR == 1 { print $2 }')"
  fi
  case "$systemd_version" in
    ''|*[!0-9.]* ) systemd_version=unknown ;;
  esac
  local archive_name
  archive_name="$(basename "$archive")"
  case "$archive_name" in
    ''|*[!A-Za-z0-9._-]* ) archive_name=invalid ;;
  esac
  local archive_sha
  archive_sha="$(sha256sum "$archive" | awk '{ print $1 }')"
  cat > "$diagnostics/summary.json" <<EOF
{"archive_name":"$archive_name","archive_sha256":"$archive_sha","manager_ready":$manager_ready,"systemd_version":"$systemd_version","test_exit_code":$result}
EOF
}

session="$workspace/session.sh"
cat > "$session" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

manager_pid=""
cleanup() {
  local result=$?
  if [ -n "$manager_pid" ]; then
    systemctl --user exit >/dev/null 2>&1 || true
    wait "$manager_pid" >/dev/null 2>&1 || true
  fi
  exit "$result"
}
trap cleanup EXIT INT TERM

systemd --user >/dev/null 2>&1 &
manager_pid=$!
deadline=$((SECONDS + 30))
until systemctl --user show-environment >/dev/null 2>&1 && \
  busctl --user call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.DBus.Peer Ping >/dev/null 2>&1; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    exit 1
  fi
  sleep 1
done
touch "$POWERCONTEXT_SYSTEMD_MANAGER_READY"
timeout 90 "$POWERCONTEXT_SYSTEMD_TEST_BINARY" -test.v -test.run '^TestReleaseArchiveProvidesConsumablePersonalService$'
EOF
chmod 0700 "$session"

result=0
if ! env -i \
  HOME="$home" \
  TMPDIR="$temporary" \
  XDG_RUNTIME_DIR="$runtime" \
  PATH=/usr/sbin:/usr/bin:/sbin:/bin \
  LC_ALL=C \
  POWERCONTEXT_PERSONAL_SERVICE_ARCHIVE="$archive" \
  POWERCONTEXT_SYSTEMD_MANAGER_READY="$workspace/manager-ready" \
  POWERCONTEXT_SYSTEMD_TEST_BINARY="$test_binary" \
  dbus-run-session -- "$session"; then
  result=1
fi
write_summary "$result"

case "$workspace" in
  "$runner_temp"/*) rm -rf -- "$workspace" ;;
  *) echo "refusing unexpected temporary cleanup target" >&2; exit 2 ;;
esac
exit "$result"
