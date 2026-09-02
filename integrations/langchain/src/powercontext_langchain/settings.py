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

"""Process configuration for the PowerContext LangChain middleware."""

from __future__ import annotations

import ipaddress
from urllib.parse import urlsplit, urlunsplit

from pydantic import Field, HttpUrl, SecretStr, TypeAdapter, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

_HTTP_URL_ADAPTER = TypeAdapter(HttpUrl)


class PowerContextLangChainSettings(BaseSettings):
    """PowerContext settings read from ``POWERCONTEXT_LANGCHAIN_*`` variables."""

    model_config = SettingsConfigDict(
        env_prefix="POWERCONTEXT_LANGCHAIN_",
        extra="ignore",
        env_ignore_empty=True,
        frozen=True,
        hide_input_in_errors=True,
    )

    base_url: str = "http://127.0.0.1:8000"
    token: SecretStr | None = Field(default=None, repr=False)
    scope_id: str | None = None
    timeout: float = Field(default=10.0, gt=0)
    max_bytes: int = Field(default=8000, ge=512, le=32768)

    @field_validator("base_url")
    @classmethod
    def validate_base_url(cls, value: str) -> str:
        return normalize_base_url(value)


def normalize_base_url(value: str) -> str:
    normalized = str(_HTTP_URL_ADAPTER.validate_python(value.strip())).rstrip("/")
    parsed = urlsplit(normalized)
    if parsed.username is not None or parsed.password is not None:
        raise ValueError("PowerContext Server URL must not contain credentials")  # noqa: TRY003
    if parsed.hostname is None or parsed.scheme not in {"http", "https"}:
        raise ValueError("PowerContext Server URL must use HTTP or HTTPS")  # noqa: TRY003
    if parsed.query or parsed.fragment:
        raise ValueError("PowerContext Server URL must not contain a query or fragment")  # noqa: TRY003
    if parsed.scheme == "http" and not _is_loopback_host(parsed.hostname):
        raise ValueError("PowerContext Server URL must use HTTPS unless the host is loopback")  # noqa: TRY003
    return urlunsplit((parsed.scheme, parsed.netloc, parsed.path.rstrip("/"), "", ""))


def _is_loopback_host(host: str) -> bool:
    if host.casefold() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


__all__ = ["PowerContextLangChainSettings"]
