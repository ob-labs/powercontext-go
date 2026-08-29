/*
 * Copyright (c) 2026 OceanBase.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { PowerContextClient } from '../src/client.ts'
import { resolveConfig } from '../src/config.ts'

type TransportVectors = { loopback: string[]; non_loopback: string[] }

const vectors = JSON.parse(readFileSync(
  new URL('../../../../../test/transport/testdata/loopback_hosts.json', import.meta.url),
  'utf8',
)) as TransportVectors

describe('OpenCode transport policy', () => {
  it.each(vectors.loopback)('accepts plaintext loopback host %s', (host) => {
    const baseUrl = `http://${host}:8000`
    expect(() => resolveConfig({ POWERCONTEXT_OPENCODE_BASE_URL: baseUrl })).not.toThrow()
    expect(() => new PowerContextClient({ baseUrl, requestTimeoutMs: 1000 })).not.toThrow()
  })

  it.each(vectors.non_loopback)('rejects plaintext non-loopback host %s', (host) => {
    const baseUrl = `http://${host}:8000`
    expect(() => resolveConfig({ POWERCONTEXT_OPENCODE_BASE_URL: baseUrl })).toThrow(/HTTPS|loopback/)
    expect(() => new PowerContextClient({ baseUrl, requestTimeoutMs: 1000 })).toThrow(/HTTPS|loopback/)
  })

  it('accepts HTTPS outside loopback', () => {
    expect(resolveConfig({ POWERCONTEXT_OPENCODE_BASE_URL: 'https://memory.example:8000' }).baseUrl)
      .toBe('https://memory.example:8000')
  })
})
