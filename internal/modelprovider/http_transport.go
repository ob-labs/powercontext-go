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

package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ob-labs/powercontext-go/inference"
)

const maxProviderResponseBytes int64 = 8 << 20

var defaultProviderHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// providerHTTPClient is intentionally local to modelprovider. It is the small
// transport surface shared by providers without a stable official Go SDK; it
// never retains response bodies in returned errors.
type providerHTTPClient struct {
	client  *http.Client
	baseURL *url.URL
	headers http.Header
}

func newProviderHTTPClient(client *http.Client, baseURL string, headers http.Header) (providerHTTPClient, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return providerHTTPClient{}, inference.NewConfigurationError("model", "invalid provider base URL")
	}
	if client == nil {
		client = defaultProviderHTTPClient
	}
	return providerHTTPClient{client: client, baseURL: parsed, headers: headers.Clone()}, nil
}

func (c providerHTTPClient) postJSON(ctx context.Context, path string, input, output any, operation string) error {
	if path == "" || !strings.HasPrefix(path, "/") || strings.Contains(path, "?") || strings.Contains(path, "#") {
		return inference.NewConfigurationError("model", "invalid provider endpoint path")
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(input); err != nil {
		return inference.NewConfigurationError("request-rejected", "provider request could not be encoded")
	}

	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), &encoded)
	if err != nil {
		return inference.NewConfigurationError("model", "provider request could not be constructed")
	}
	request.Header = c.headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return inference.WrapUnavailableError(operation, err)
	}
	defer func() { _ = response.Body.Close() }()

	limited := io.LimitReader(response.Body, maxProviderResponseBytes+1)
	body, readErr := io.ReadAll(limited)
	if readErr != nil {
		return inference.WrapUnavailableError(operation, readErr)
	}
	if int64(len(body)) > maxProviderResponseBytes {
		return inference.NewInvalidOutputError(operation, "provider response exceeded the size limit")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return mapProviderHTTPStatus(response.StatusCode, operation, nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(output); err != nil {
		return inference.NewInvalidOutputError(operation, "provider returned invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return inference.NewInvalidOutputError(operation, "provider returned trailing JSON data")
	}
	return nil
}

func mapProviderHTTPStatus(status int, operation string, cause error) error {
	switch {
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return context.DeadlineExceeded
	case status == http.StatusConflict || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
		if cause != nil {
			return inference.WrapUnavailableError(operation, cause)
		}
		return inference.NewUnavailableError(operation)
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		detail := fmt.Sprintf("HTTP %d", status)
		if cause != nil {
			return inference.WrapConfigurationError("provider-rejected", detail, cause)
		}
		return inference.NewConfigurationError("provider-rejected", detail)
	default:
		if cause != nil {
			return inference.WrapUnavailableError(operation, cause)
		}
		return inference.NewUnavailableError(operation)
	}
}
