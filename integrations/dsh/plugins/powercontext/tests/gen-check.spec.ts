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

import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { checkGenerated, generateOperations, renderGeneratedSource } from '../scripts/gen-operations.mjs'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..')

describe('generated operations check', () => {
  it('passes when the committed table matches OpenAPI', () => {
    expect(() => checkGenerated()).not.toThrow()
  })

  it('fails when the committed table has drifted', () => {
    const dir = mkdtempSync(join(tmpdir(), 'pc-gen-'))
    const drifted = join(dir, 'operations.generated.ts')
    writeFileSync(drifted, 'export const OPERATIONS = {}\n')
    expect(() => checkGenerated(drifted)).toThrow(/drifted/)
  })

  it('renders the same source the repository currently commits', () => {
    const committed = readFileSync(join(repoRoot, 'src', 'operations.generated.ts'), 'utf8').replace(/\r\n/g, '\n')
    expect(renderGeneratedSource()).toBe(committed)
  })

  it('writes a fresh generated table to an explicit output path', () => {
    const dir = mkdtempSync(join(tmpdir(), 'pc-gen-output-'))
    const output = join(dir, 'operations.generated.ts')
    expect(generateOperations(output)).toBe(output)
    expect(existsSync(output)).toBe(true)
    expect(readFileSync(output, 'utf8')).toBe(renderGeneratedSource())

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
  })
})
