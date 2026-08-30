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

import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it } from 'vitest'
import { checkGenerated, renderGeneratedSource } from '../scripts/gen-operations.mjs'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const generatorScript = join(repoRoot, 'scripts', 'gen-operations.mjs')
const tempDirs: string[] = []
const processTestTimeoutMs = 60_000

function makeTempDir(prefix: string) {
  const dir = mkdtempSync(join(tmpdir(), prefix))
  tempDirs.push(dir)
  return dir
}

function normalizeNewlines(text: string) {
  return text.replace(/\r\n/g, '\n')
}

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true })
  }
})

describe('generated operations check', () => {
  it('passes when the committed table matches OpenAPI', () => {
    expect(() => checkGenerated()).not.toThrow()
  })

  it('fails when an explicit CLI output has drifted', () => {
    const dir = makeTempDir('pc-gen-')
    const drifted = join(dir, 'operations.generated.ts')
    writeFileSync(drifted, 'export const OPERATIONS = {}\n')
    const result = spawnSync(process.execPath, [generatorScript, '--check', '--output', drifted], {
      encoding: 'utf8',
    })

    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('Generated API code drifted')
  }, processTestTimeoutMs)

  it('renders the same source the repository currently commits', () => {
    const committed = normalizeNewlines(readFileSync(join(repoRoot, 'src', 'operations.generated.ts'), 'utf8'))
    expect(renderGeneratedSource()).toBe(committed)
  })

  it('writes and checks a fresh generated table through the public CLI', () => {
    const dir = makeTempDir('pc-gen-output-')
    const output = join(dir, 'operations.generated.ts')
    execFileSync(process.execPath, [generatorScript, '--output', output])
    const committed = normalizeNewlines(readFileSync(join(repoRoot, 'src', 'operations.generated.ts'), 'utf8'))
    expect(normalizeNewlines(readFileSync(output, 'utf8'))).toBe(committed)
    execFileSync(
      process.execPath,
      [generatorScript, '--check', '--output', output],
    )

    writeFileSync(join(dir, 'consumer.ts'), `
import { OPERATION_IDS, OPERATIONS } from './operations.generated.ts'

const first = OPERATION_IDS[0]
const operation = OPERATIONS[first]
void operation
`)
    writeFileSync(join(dir, 'tsconfig.json'), JSON.stringify({
      compilerOptions: {
        allowImportingTsExtensions: true,
        module: 'ESNext',
        moduleResolution: 'bundler',
        noEmit: true,
        strict: true,
        target: 'ES2022',
      },
      include: ['consumer.ts', 'operations.generated.ts'],
    }))
    execFileSync(process.execPath, [join(repoRoot, 'node_modules', 'typescript', 'bin', 'tsc'), '-p', join(dir, 'tsconfig.json')])
  }, processTestTimeoutMs)

  it('rejects another option where --output requires a path', () => {
    const dir = makeTempDir('pc-gen-missing-output-')
    execFileSync(process.execPath, [generatorScript, '--output', join(dir, '--check')])

    const result = spawnSync(process.execPath, [generatorScript, '--output', '--check'], {
      cwd: dir,
      encoding: 'utf8',
    })

    expect(result.status).not.toBe(0)
    expect(result.stderr).toContain('gen-operations: --output requires a path')
    expect(result.stdout).not.toContain('generated operations are current')
  }, processTestTimeoutMs)
})
