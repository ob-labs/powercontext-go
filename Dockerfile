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

ARG GO_VERSION=1.27.0
ARG GO_IMAGE=golang
ARG DEBIAN_VERSION=bookworm-slim
ARG DEBIAN_IMAGE=debian
ARG DISTROLESS_IMAGE=gcr.io/distroless/cc-debian12:nonroot

FROM ${DEBIAN_IMAGE}:${DEBIAN_VERSION} AS runtime-files
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o 65532 -g 65532 /out/data

FROM ${GO_IMAGE}:${GO_VERSION}-bookworm AS go-build
WORKDIR /src
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends libsqlite3-dev \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .

ARG VERSION=0.0.0-devel
ARG COMMIT=0000000000000000000000000000000000000000
ARG BUILD_DATE=1970-01-01T00:00:00Z
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/powercontext ./cmd/powercontext
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go run ./tools/release metadata \
    -binary /out/powercontext -edition standard \
    -version "${VERSION}" -commit "${COMMIT}" -build-date "${BUILD_DATE}" \
    -output /out/metadata-standard

FROM ${DEBIAN_IMAGE}:${DEBIAN_VERSION} AS local-runtime-assets
ARG TARGETARCH
ARG TOKENIZERS_VERSION=1.26.0
ARG TOKENIZERS_AMD64_SHA256=0713ac926d8473440572f59369a1bf2d3aa3fa5c6446eab45191f6e5f04ffeb0
ARG TOKENIZERS_ARM64_SHA256=e8a19c39bbad5b86044d98519b131806cad6b4a3e1408c11ad555b630051e49b
ARG ONNXRUNTIME_VERSION=1.24.4
ARG ONNXRUNTIME_AMD64_SHA256=3a211fbea252c1e66290658f1b735b772056149f28321e71c308942cdb54b747
ARG ONNXRUNTIME_ARM64_SHA256=866109a9248d057671a039b9d725be4bd86888e3754140e6701ec621be9d4d7e
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN case "${TARGETARCH}" in \
      amd64) powercontext_tokenizer_arch=x86_64; powercontext_tokenizer_sha="${TOKENIZERS_AMD64_SHA256}"; powercontext_ort_arch=x64; powercontext_ort_sha="${ONNXRUNTIME_AMD64_SHA256}" ;; \
      arm64) powercontext_tokenizer_arch=arm64; powercontext_tokenizer_sha="${TOKENIZERS_ARM64_SHA256}"; powercontext_ort_arch=aarch64; powercontext_ort_sha="${ONNXRUNTIME_ARM64_SHA256}" ;; \
      *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 2 ;; \
    esac \
    && curl --fail --location --retry 3 --output /tmp/tokenizers.tar.gz \
      "https://github.com/daulet/tokenizers/releases/download/v${TOKENIZERS_VERSION}/libtokenizers.linux-${powercontext_tokenizer_arch}.tar.gz" \
    && echo "${powercontext_tokenizer_sha}  /tmp/tokenizers.tar.gz" | sha256sum --check --strict \
    && curl --fail --location --retry 3 --output /tmp/onnxruntime.tgz \
      "https://github.com/microsoft/onnxruntime/releases/download/v${ONNXRUNTIME_VERSION}/onnxruntime-linux-${powercontext_ort_arch}-${ONNXRUNTIME_VERSION}.tgz" \
    && echo "${powercontext_ort_sha}  /tmp/onnxruntime.tgz" | sha256sum --check --strict \
    && install -d /out/tokenizers /out/onnxruntime /tmp/onnxruntime \
    && tar -xzf /tmp/tokenizers.tar.gz -C /out/tokenizers libtokenizers.a \
    && tar -xzf /tmp/onnxruntime.tgz -C /tmp/onnxruntime \
    && cp -a "/tmp/onnxruntime/onnxruntime-linux-${powercontext_ort_arch}-${ONNXRUNTIME_VERSION}/lib/"libonnxruntime*.so* /out/onnxruntime/ \
    && test -n "$(find /out/onnxruntime -maxdepth 1 -type f -name 'libonnxruntime*.so*' -print -quit)"

FROM go-build AS go-build-full
COPY --from=local-runtime-assets /out/tokenizers /opt/powercontext/tokenizers
COPY --from=local-runtime-assets /out/onnxruntime /opt/powercontext/onnxruntime
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CGO_LDFLAGS=-L/opt/powercontext/tokenizers \
    go build -tags 'sqlite_fts5 local_embeddings ORT' -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
    -o /out/powercontext-full ./cmd/powercontext
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go run ./tools/release metadata \
    -binary /out/powercontext-full \
    -onnxruntime-dir /opt/powercontext/onnxruntime -edition full \
    -version "${VERSION}" -commit "${COMMIT}" -build-date "${BUILD_DATE}" \
    -output /out/metadata-full

FROM ${DISTROLESS_IMAGE} AS runtime-base
COPY --from=runtime-files /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=runtime-files /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=runtime-files --chown=65532:65532 /out/data /var/lib/powercontext
WORKDIR /var/lib/powercontext

# Published container ports require a non-loopback bind. The image declares
# the controlled-network opt-in so its documented invocation starts; operators
# should still enable bearer authentication and terminate TLS before exposure.
ENV POWERCONTEXT_SERVER_HTTP_HOST=0.0.0.0 \
    POWERCONTEXT_SERVER_HTTP_PORT=8000 \
    POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true
EXPOSE 8000
USER 65532:65532

FROM runtime-base AS powercontext
ARG VERSION=0.0.0-devel
ARG COMMIT=0000000000000000000000000000000000000000
ARG BUILD_DATE=1970-01-01T00:00:00Z
LABEL org.opencontainers.image.title="PowerContext" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/ob-labs/powercontext-go" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=go-build /out/powercontext /usr/local/bin/powercontext
COPY --from=go-build /out/metadata-standard/ /usr/share/powercontext/
COPY --from=go-build /src/openapi/powercontext.yaml /usr/share/powercontext/openapi/powercontext.yaml
COPY --from=go-build /src/LICENSE /usr/share/licenses/powercontext/LICENSE
ENTRYPOINT ["/usr/local/bin/powercontext"]

FROM runtime-base AS powercontext-full
ARG VERSION=0.0.0-devel
ARG COMMIT=0000000000000000000000000000000000000000
ARG BUILD_DATE=1970-01-01T00:00:00Z
LABEL org.opencontainers.image.title="PowerContext Full" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/ob-labs/powercontext-go" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=local-runtime-assets /out/onnxruntime/ /usr/lib/
COPY --from=go-build-full /out/powercontext-full /usr/local/bin/powercontext
COPY --from=go-build-full /out/metadata-full/ /usr/share/powercontext/
COPY --from=go-build-full /src/openapi/powercontext.yaml /usr/share/powercontext/openapi/powercontext.yaml
COPY --from=go-build-full /src/LICENSE /usr/share/licenses/powercontext/LICENSE
ENV POWERCONTEXT_ONNXRUNTIME_LIBRARY_DIR=/usr/lib
ENTRYPOINT ["/usr/local/bin/powercontext"]

# A plain `docker build .` intentionally produces the standard artifact.
FROM powercontext AS final
