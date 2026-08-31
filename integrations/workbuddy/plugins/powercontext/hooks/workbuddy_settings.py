#!/usr/bin/env python3
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

# Adapted for WorkBuddy from the PowerContext Claude Code plugin
# (integrations/claude-code/plugins/powercontext/claude_code_settings.py).

"""Validated process configuration for the PowerContext WorkBuddy hooks driver."""

from __future__ import annotations

import json
import math
import os
import re
from collections.abc import Mapping
from dataclasses import dataclass
from ipaddress import ip_address
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

_CONFIG_SCHEMA = 1
_DEFAULT_SERVER_URL = "http://127.0.0.1:8000"
_DEFAULT_AUTHORIZATION_ENVIRONMENT = "POWERCONTEXT_WORKBUDDY_AUTHORIZATION"
_DEFAULT_REQUEST_TIMEOUT_SECONDS = 1.5
_DEFAULT_REQUEST_BUDGET_SECONDS = 3.0
_DEFAULT_PREPARE_MAX_BYTES = 8_000
_DEFAULT_SOURCE_MAX_BYTES = 16_384
_MAX_REQUEST_BUDGET_SECONDS = 10.0
_MAX_PREPARE_BYTES = 32 * 1024
_MAX_SOURCE_BYTES = 64 * 1024
_LOOPBACK_HOSTS = frozenset({"127.0.0.1", "::1", "localhost"})
_TRUE_VALUES = frozenset({"1", "true", "yes", "on"})
_FALSE_VALUES = frozenset({"0", "false", "no", "off"})
_AUTHORIZATION_ENVIRONMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")
_FIELDS = frozenset(
    {
        "schema",
        "server_url",
        "scope_mode",
        "authorization_environment",
        "request_timeout_seconds",
        "request_budget_seconds",
        "prepare_max_bytes",
        "source_max_bytes",
    }
)


class WorkBuddyConfigurationError(ValueError):
    """Raised when the persisted WorkBuddy configuration is unsafe or invalid."""


@dataclass(frozen=True, slots=True)
class WorkBuddyPluginSettings:
    """Configuration loaded once by a WorkBuddy hooks entry point."""

    server_url: str = _DEFAULT_SERVER_URL
    authorization: str | None = None
    authorization_environment: str = _DEFAULT_AUTHORIZATION_ENVIRONMENT
    scope_id: str | None = None
    scope_mode: str = "project"
    capture_prompts: bool = True
    flush_on_capture: bool = False
    request_timeout_seconds: float = _DEFAULT_REQUEST_TIMEOUT_SECONDS
    request_budget_seconds: float = _DEFAULT_REQUEST_BUDGET_SECONDS
    prepare_max_bytes: int = _DEFAULT_PREPARE_MAX_BYTES
    source_max_bytes: int = _DEFAULT_SOURCE_MAX_BYTES
    flush_max_calls: int = 4

    def __post_init__(self) -> None:
        object.__setattr__(self, "server_url", _http_base_url(self.server_url))
        object.__setattr__(self, "authorization", _authorization_header(self.authorization))
        object.__setattr__(self, "authorization_environment", _authorization_environment_name(self.authorization_environment))
        object.__setattr__(self, "scope_id", _optional_text(self.scope_id))
        _validate_scope_mode(self.scope_mode)
        _validate_limits(
            self.request_timeout_seconds,
            self.request_budget_seconds,
            self.prepare_max_bytes,
            self.source_max_bytes,
            self.flush_max_calls,
        )

    @property
    def http_budget_seconds(self) -> float:
        """Compatibility alias for the existing hook implementation."""

        return self.request_budget_seconds

    @classmethod
    def from_environment(cls) -> WorkBuddyPluginSettings:
        """Load WorkBuddy user options and integration-specific environment values."""

        return cls(
            server_url=_first_environment("POWERCONTEXT_WORKBUDDY_SERVER_URL") or _DEFAULT_SERVER_URL,
            authorization=_first_environment("POWERCONTEXT_WORKBUDDY_AUTHORIZATION"),
            scope_id=_first_environment("POWERCONTEXT_WORKBUDDY_SCOPE_ID"),
            capture_prompts=_environment_bool(
                "POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS",
                default=True,
            ),
            flush_on_capture=_environment_bool(
                "POWERCONTEXT_WORKBUDDY_FLUSH_ON_CAPTURE",
                default=False,
            ),
            request_timeout_seconds=_environment_float(
                "POWERCONTEXT_WORKBUDDY_REQUEST_TIMEOUT_SECONDS",
                default=_DEFAULT_REQUEST_TIMEOUT_SECONDS,
            ),
            request_budget_seconds=_environment_float(
                "POWERCONTEXT_WORKBUDDY_HTTP_BUDGET_SECONDS",
                "POWERCONTEXT_WORKBUDDY_REQUEST_BUDGET_SECONDS",
                default=_DEFAULT_REQUEST_BUDGET_SECONDS,
            ),
            prepare_max_bytes=_environment_int(
                "POWERCONTEXT_WORKBUDDY_PREPARE_MAX_BYTES",
                default=_DEFAULT_PREPARE_MAX_BYTES,
            ),
            source_max_bytes=_environment_int(
                "POWERCONTEXT_WORKBUDDY_SOURCE_MAX_BYTES",
                default=_DEFAULT_SOURCE_MAX_BYTES,
            ),
            flush_max_calls=_environment_int(
                "POWERCONTEXT_WORKBUDDY_FLUSH_MAX_CALLS",
                default=4,
            ),
        )


def load_settings(
    path: str | os.PathLike[str],
    *,
    environment: Mapping[str, str] | None = None,
) -> WorkBuddyPluginSettings:
    """Load the credential-free setup configuration and runtime credentials."""

    configuration_path = Path(path)
    try:
        payload = json.loads(configuration_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise WorkBuddyConfigurationError("invalid WorkBuddy configuration") from error
    if not isinstance(payload, dict) or set(payload) != _FIELDS:
        raise WorkBuddyConfigurationError("invalid WorkBuddy configuration")
    if payload["schema"] != _CONFIG_SCHEMA:
        raise WorkBuddyConfigurationError("invalid WorkBuddy configuration schema")
    authorization_environment = _text_field(payload, "authorization_environment")
    runtime_environment = os.environ if environment is None else environment
    return WorkBuddyPluginSettings(
        server_url=_text_field(payload, "server_url"),
        authorization=runtime_environment.get(authorization_environment),
        authorization_environment=authorization_environment,
        scope_mode=_text_field(payload, "scope_mode"),
        request_timeout_seconds=_float_field(payload, "request_timeout_seconds"),
        request_budget_seconds=_float_field(payload, "request_budget_seconds"),
        prepare_max_bytes=_int_field(payload, "prepare_max_bytes"),
        source_max_bytes=_int_field(payload, "source_max_bytes"),
    )


def _first_environment(*names: str) -> str | None:
    for name in names:
        value = os.environ.get(name)
        if value is not None:
            return value
    return None


def _text_field(payload: Mapping[str, object], name: str) -> str:
    value = payload[name]
    if not isinstance(value, str):
        raise WorkBuddyConfigurationError(f"invalid WorkBuddy {name.replace('_', ' ')}")
    return value


def _float_field(payload: Mapping[str, object], name: str) -> float:
    value = payload[name]
    if not isinstance(value, int | float) or isinstance(value, bool):
        raise WorkBuddyConfigurationError(f"invalid WorkBuddy {name.replace('_', ' ')}")
    return float(value)


def _int_field(payload: Mapping[str, object], name: str) -> int:
    value = payload[name]
    if not isinstance(value, int) or isinstance(value, bool):
        raise WorkBuddyConfigurationError(f"invalid WorkBuddy {name.replace('_', ' ')}")
    return value


def _environment_bool(*names: str, default: bool) -> bool:
    value = _first_environment(*names)
    if value is None:
        return default
    normalized = value.strip().casefold()
    if normalized in _TRUE_VALUES:
        return True
    if normalized in _FALSE_VALUES:
        return False
    raise WorkBuddyConfigurationError("invalid boolean PowerContext configuration")


def _environment_float(*names: str, default: float) -> float:
    value = _first_environment(*names)
    return default if value is None else float(value)


def _environment_int(name: str, *, default: int) -> int:
    value = os.environ.get(name)
    return default if value is None else int(value)


def _optional_text(value: str | None) -> str | None:
    if value is None:
        return None
    return value.strip() or None


def _authorization_header(value: str | None) -> str | None:
    normalized = _optional_text(value)
    if normalized is None:
        return None
    scheme, separator, credential = normalized.partition(" ")
    if (
        not separator
        or scheme.casefold() != "bearer"
        or not credential
        or not credential.isascii()
        or not credential.isprintable()
        or any(character.isspace() for character in credential)
    ):
        raise WorkBuddyConfigurationError("WorkBuddy authorization must be a valid Bearer header")
    return normalized


def _authorization_environment_name(value: str) -> str:
    normalized = value.strip()
    if not _AUTHORIZATION_ENVIRONMENT.fullmatch(normalized):
        raise WorkBuddyConfigurationError("invalid WorkBuddy authorization environment name")
    return normalized


def _validate_scope_mode(value: str) -> None:
    if value not in {"agent", "project"}:
        raise WorkBuddyConfigurationError("WorkBuddy scope must be agent or project")


def _validate_limits(
    request_timeout_seconds: float,
    request_budget_seconds: float,
    prepare_max_bytes: int,
    source_max_bytes: int,
    flush_max_calls: int,
) -> None:
    if (
        math.isnan(request_timeout_seconds)
        or math.isinf(request_timeout_seconds)
        or request_timeout_seconds <= 0
    ):
        raise WorkBuddyConfigurationError("WorkBuddy request timeout must be positive")
    if (
        math.isnan(request_budget_seconds)
        or math.isinf(request_budget_seconds)
        or request_budget_seconds <= 0
        or request_budget_seconds > _MAX_REQUEST_BUDGET_SECONDS
    ):
        raise WorkBuddyConfigurationError("WorkBuddy request budget is invalid")
    if request_timeout_seconds > request_budget_seconds:
        raise WorkBuddyConfigurationError("WorkBuddy request timeout must not exceed the budget")
    if prepare_max_bytes < 1 or prepare_max_bytes > _MAX_PREPARE_BYTES:
        raise WorkBuddyConfigurationError("WorkBuddy prepare byte limit is invalid")
    if source_max_bytes < 1 or source_max_bytes > _MAX_SOURCE_BYTES:
        raise WorkBuddyConfigurationError("WorkBuddy source byte limit is invalid")
    if not 1 <= flush_max_calls <= 16:
        raise WorkBuddyConfigurationError("PowerContext flush_max_calls must be between 1 and 16")


def _http_base_url(value: str) -> str:
    normalized = value.strip().rstrip("/")
    parsed = urlsplit(normalized)
    if parsed.username is not None or parsed.password is not None:
        raise WorkBuddyConfigurationError("PowerContext Server URL must not contain credentials")
    if parsed.hostname is None or parsed.scheme not in {"http", "https"}:
        raise WorkBuddyConfigurationError("PowerContext Server URL must use HTTP or HTTPS")
    if parsed.query or parsed.fragment:
        raise WorkBuddyConfigurationError("PowerContext Server URL must not contain a query or fragment")
    if parsed.scheme == "http" and not _is_loopback_host(parsed.hostname):
        raise WorkBuddyConfigurationError("unencrypted PowerContext URLs must be loopback addresses")
    path = parsed.path.rstrip("/")
    if path.endswith("/mcp"):
        path = path.removesuffix("/mcp")
    return urlunsplit((parsed.scheme, parsed.netloc, path, "", "")).rstrip("/")


def _is_loopback_host(value: str) -> bool:
    host = value.lower()
    if host in _LOOPBACK_HOSTS:
        return True
    try:
        return ip_address(host).is_loopback
    except ValueError:
        return False


__all__ = ["WorkBuddyConfigurationError", "WorkBuddyPluginSettings", "load_settings"]
