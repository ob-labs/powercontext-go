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

import { existsSync } from 'node:fs'
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import { startPowerContextServer } from '../scripts/e2e-server.mjs'

const createdPaths: string[] = []

afterEach(async () => {
  await Promise.all(createdPaths.splice(0).map((path) => rm(path, { force: true, recursive: true })))
})

describe('DSH E2E Server startup diagnostics', () => {
  it('reports an early child exit with bounded server logs', async () => {
    const fixtureDirectory = await mkdtemp(join(tmpdir(), 'powercontext-dsh-e2e-server-'))
    createdPaths.push(fixtureDirectory)
    const fixture = join(fixtureDirectory, 'exit-seven.mjs')
    const marker = 'dsh-startup-fixture-marker'
    await writeFile(
      fixture,
      `process.stderr.write(${JSON.stringify(marker + ' ' + 'x'.repeat(40 * 1024))}); process.exit(7)`,
    )

    const error = await startPowerContextServer({
      command: { command: process.execPath, args: [fixture] },
      timeoutMs: 250,
    }).catch((reason) => reason as Error)

    expect(error).toBeInstanceOf(Error)
    expect(error.message).toContain('exit code 7')
    expect(error.message).toContain(marker)
    expect(Buffer.byteLength(error.message)).toBeLessThanOrEqual(34 * 1024)
  })

  it('removes a failed startup home directory', async () => {
    const fixtureDirectory = await mkdtemp(join(tmpdir(), 'powercontext-dsh-e2e-server-'))
    createdPaths.push(fixtureDirectory)
    const fixture = join(fixtureDirectory, 'exit-seven.mjs')
    const home = join(fixtureDirectory, 'home')
    await Promise.all([
      writeFile(fixture, 'process.exit(7)'),
      mkdir(home),
    ])

    await expect(startPowerContextServer({
      command: { command: process.execPath, args: [fixture] },
      home,
      timeoutMs: 250,
    })).rejects.toThrow('exit code 7')

    expect(existsSync(home)).toBe(false)
  })
})
