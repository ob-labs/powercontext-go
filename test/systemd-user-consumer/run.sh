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
temporary="$workspace/tmp"
mkdir -m 0700 "$temporary"
cp -- "$archive" "$workspace/$(basename "$archive")"
cp -- "$test_binary" "$workspace/$(basename "$test_binary")"
chmod 0400 "$workspace/$(basename "$archive")"
chmod 0500 "$workspace/$(basename "$test_binary")"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
container="powercontext-systemd-user-${RANDOM}${RANDOM}"
image="powercontext-systemd-user-consumer:local"
systemd_version=unknown
recorded_stage=not_started
stage_exit_code=0
stage_stdout_bytes=0
stage_stdout_sha256="$(printf '' | sha256sum | awk '{ print substr($1, 1, 16) }')"
stage_output="$workspace/.stage-output"
load_unit_stage=not_run
load_unit_exit_code=0
load_unit_stdout_bytes=0
load_unit_stdout_sha256="$stage_stdout_sha256"
unit_properties_stage=not_run
unit_properties_exit_code=0
unit_properties_stdout_bytes=0
unit_properties_stdout_sha256="$stage_stdout_sha256"
service_properties_stage=not_run
service_properties_exit_code=0
service_properties_stdout_bytes=0
service_properties_stdout_sha256="$stage_stdout_sha256"
load_state_stage=not_run
load_state_exit_code=0
load_state_stdout_bytes=0
load_state_stdout_sha256="$stage_stdout_sha256"
load_state_value=not_run
show_state_stage=not_run
show_state_exit_code=0
show_state_stdout_bytes=0
show_state_stdout_sha256="$stage_stdout_sha256"
show_state_value=not_run

record_stage() {
  local name="$1"
  shift
  local exit_code
  : > "$stage_output"
  if "$@" > "$stage_output" 2>/dev/null; then
    exit_code=0
  else
    exit_code=$?
  fi
  recorded_stage="$name"
  stage_exit_code="$exit_code"
  stage_stdout_bytes="$(wc -c < "$stage_output" | tr -d '[:space:]')"
  stage_stdout_sha256="$(sha256sum "$stage_output" | awk '{ print substr($1, 1, 16) }')"
  rm -f -- "$stage_output"
  return "$exit_code"
}

record_diagnostic_stage() {
  local name="$1"
  shift
  if record_stage "$name" "$@"; then
    :
  fi
  case "$name" in
    load_unit)
      load_unit_stage="$recorded_stage"
      load_unit_exit_code="$stage_exit_code"
      load_unit_stdout_bytes="$stage_stdout_bytes"
      load_unit_stdout_sha256="$stage_stdout_sha256"
      ;;
    unit_properties)
      unit_properties_stage="$recorded_stage"
      unit_properties_exit_code="$stage_exit_code"
      unit_properties_stdout_bytes="$stage_stdout_bytes"
      unit_properties_stdout_sha256="$stage_stdout_sha256"
      ;;
    service_properties)
      service_properties_stage="$recorded_stage"
      service_properties_exit_code="$stage_exit_code"
      service_properties_stdout_bytes="$stage_stdout_bytes"
      service_properties_stdout_sha256="$stage_stdout_sha256"
      ;;
  esac
}

record_load_state() {
  local name="$1"
  shift
  local exit_code
  : > "$stage_output"
  if "$@" > "$stage_output" 2>/dev/null; then
    exit_code=0
  else
    exit_code=$?
  fi
  recorded_stage="$name"
  stage_exit_code="$exit_code"
  stage_stdout_bytes="$(wc -c < "$stage_output" | tr -d '[:space:]')"
  stage_stdout_sha256="$(sha256sum "$stage_output" | awk '{ print substr($1, 1, 16) }')"
  load_state_stage="$recorded_stage"
  load_state_exit_code="$stage_exit_code"
  load_state_stdout_bytes="$stage_stdout_bytes"
  load_state_stdout_sha256="$stage_stdout_sha256"
  if [ "$exit_code" -eq 0 ]; then
    case "$(tr -d '\r\n' < "$stage_output")" in
      not-found) load_state_value=not_found ;;
      loaded) load_state_value=loaded ;;
      '') load_state_value=empty ;;
      *) load_state_value=other ;;
    esac
  fi
  rm -f -- "$stage_output"
}

record_show_state() {
  local saved_stage="$load_state_stage"
  local saved_exit_code="$load_state_exit_code"
  local saved_stdout_bytes="$load_state_stdout_bytes"
  local saved_stdout_sha256="$load_state_stdout_sha256"
  local saved_value="$load_state_value"
  record_load_state "$@"
  show_state_stage="$load_state_stage"
  show_state_exit_code="$load_state_exit_code"
  show_state_stdout_bytes="$load_state_stdout_bytes"
  show_state_stdout_sha256="$load_state_stdout_sha256"
  show_state_value="$load_state_value"
  load_state_stage="$saved_stage"
  load_state_exit_code="$saved_exit_code"
  load_state_stdout_bytes="$saved_stdout_bytes"
  load_state_stdout_sha256="$saved_stdout_sha256"
  load_state_value="$saved_value"
}

write_summary() {
  local result="$1"
  local manager_ready=false
  if [ -f "$workspace/manager-ready" ]; then
    manager_ready=true
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
{"archive_name":"$archive_name","archive_sha256":"$archive_sha","manager_ready":$manager_ready,"stage":"$recorded_stage","stage_exit_code":$stage_exit_code,"stage_stdout_bytes":$stage_stdout_bytes,"stage_stdout_sha256":"$stage_stdout_sha256","load_unit":{"stage":"$load_unit_stage","exit_code":$load_unit_exit_code,"stdout_bytes":$load_unit_stdout_bytes,"stdout_sha256":"$load_unit_stdout_sha256"},"unit_properties":{"stage":"$unit_properties_stage","exit_code":$unit_properties_exit_code,"stdout_bytes":$unit_properties_stdout_bytes,"stdout_sha256":"$unit_properties_stdout_sha256"},"service_properties":{"stage":"$service_properties_stage","exit_code":$service_properties_exit_code,"stdout_bytes":$service_properties_stdout_bytes,"stdout_sha256":"$service_properties_stdout_sha256"},"load_state":{"stage":"$load_state_stage","exit_code":$load_state_exit_code,"stdout_bytes":$load_state_stdout_bytes,"stdout_sha256":"$load_state_stdout_sha256","value":"$load_state_value"},"show_state":{"stage":"$show_state_stage","exit_code":$show_state_exit_code,"stdout_bytes":$show_state_stdout_bytes,"stdout_sha256":"$show_state_stdout_sha256","value":"$show_state_value"},"systemd_version":"$systemd_version","test_exit_code":$result}
EOF
}

result=0
if ! record_stage image_build docker build --quiet --file "$script_dir/Dockerfile" --tag "$image" "$script_dir"; then
  result=1
elif ! record_stage container_start docker run --detach --privileged --tmpfs /run --tmpfs /run/lock \
  --name "$container" --volume "$workspace:/work" "$image"; then
  result=1
else
  if ! record_stage user_runtime_directory docker exec "$container" install -d --owner=powercontext --group=powercontext --mode=0700 /run/user/1001; then
    result=1
  elif ! record_stage user_manager_start docker exec "$container" systemctl start user@1001.service; then
    result=1
  else
    systemd_version="$(docker exec "$container" systemd --version 2>/dev/null | awk 'NR == 1 { print $2 }')"
    environment=(
      HOME=/home/powercontext
      TMPDIR=/work/tmp
      XDG_RUNTIME_DIR=/run/user/1001
      DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus
      PATH=/usr/sbin:/usr/bin:/sbin:/bin
      LC_ALL=C
    )
    if ! record_stage manager_environment docker exec --user powercontext "$container" env -i "${environment[@]}" systemctl --user show-environment; then
      result=1
    elif ! record_stage user_dbus_socket_start docker exec --user powercontext "$container" env -i "${environment[@]}" systemctl --user start dbus.socket; then
      result=1
    elif ! record_stage user_dbus_socket docker exec --user powercontext "$container" env -i "${environment[@]}" test -S /run/user/1001/bus; then
      result=1
    elif ! record_stage user_bus_ping docker exec --user powercontext "$container" env -i "${environment[@]}" \
      busctl --user --json=short call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.DBus.Peer Ping; then
      result=1
    elif ! record_stage unowned_unit_path docker exec "$container" test ! -e /home/powercontext/.config/systemd/user/powercontext.service; then
      result=1
    else
      touch "$workspace/manager-ready"
      if ! record_stage archive_consumer docker exec --user powercontext "$container" env -i "${environment[@]}" \
        POWERCONTEXT_PERSONAL_SERVICE_ARCHIVE=/work/"$(basename "$archive")" \
        timeout 90 /work/"$(basename "$test_binary")" -test.v -test.run '^TestReleaseArchiveProvidesConsumablePersonalService$'; then
        result=1
        record_diagnostic_stage load_unit docker exec --user powercontext "$container" env -i "${environment[@]}" \
          busctl --user --json=short call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager LoadUnit s powercontext.service
        record_diagnostic_stage unit_properties docker exec --user powercontext "$container" env -i "${environment[@]}" \
          busctl --user --json=short call org.freedesktop.systemd1 /org/freedesktop/systemd1/unit/powercontext_2eservice org.freedesktop.DBus.Properties GetAll s org.freedesktop.systemd1.Unit
        record_diagnostic_stage service_properties docker exec --user powercontext "$container" env -i "${environment[@]}" \
          busctl --user --json=short call org.freedesktop.systemd1 /org/freedesktop/systemd1/unit/powercontext_2eservice org.freedesktop.DBus.Properties GetAll s org.freedesktop.systemd1.Service
        record_load_state load_state docker exec --user powercontext "$container" env -i "${environment[@]}" \
          systemctl --user show --property=LoadState --value powercontext.service
        record_show_state show_state docker exec --user powercontext "$container" env -i "${environment[@]}" \
          systemctl --user show --property=LoadState --property=FragmentPath --property=DropInPaths --property=Environment --property=ExecStart powercontext.service
      fi
    fi
  fi
fi
write_summary "$result"

docker rm --force "$container" >/dev/null 2>&1 || true

case "$workspace" in
  "$runner_temp"/*) rm -rf -- "$workspace" ;;
  *) echo "refusing unexpected temporary cleanup target" >&2; exit 2 ;;
esac
exit "$result"
