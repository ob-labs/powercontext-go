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

import { createServer } from 'node:net'
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { resolvePowerContextRoot } from './sync-openapi.mjs'

const pluginRoot = join(dirname(fileURLToPath(import.meta.url)), '..')
const maxStartupLogBytes = 32 * 1024

function walkForGoModule(startDir) {
  let dir = resolve(startDir)
  for (let i = 0; i < 8; i += 1) {
    if (existsSync(join(dir, 'go.mod')) && existsSync(join(dir, 'cmd', 'powercontext'))) return dir
    const parent = resolve(dir, '..')
    if (parent === dir) break
    dir = parent
  }
  return undefined
}

export function defaultPowerContextRoot() {
  const fromResolver = resolvePowerContextRoot()
  if (fromResolver && existsSync(join(fromResolver, 'go.mod'))) return fromResolver
  return walkForGoModule(pluginRoot)
}

export function unusedPort() {
  return new Promise((resolve, reject) => {
    const server = createServer()
    server.listen(0, '127.0.0.1', () => {
      const address = server.address()
      if (!address || typeof address === 'string') {
        server.close()
        reject(new Error('could not allocate a TCP port'))
        return
      }
      const { port } = address
      server.close((error) => {
        if (error) reject(error)
        else resolve(port)
      })
    })
    server.on('error', reject)
  })
}

export async function waitForUrl(url, timeoutMs = 30000, signal) {
  const deadline = Date.now() + timeoutMs
  let lastError
  while (Date.now() < deadline) {
    if (signal?.aborted) throw signal.reason
    try {
      const response = await fetch(url, {
        signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(1000)]) : AbortSignal.timeout(1000),
      })
      if (response.ok || response.status === 503) return
      lastError = new Error(`HTTP ${response.status}`)
    } catch (error) {
      if (signal?.aborted) throw signal.reason
      lastError = error
    }
    await delay(200, signal)
  }
  throw new Error(`Server at ${url} did not become ready: ${lastError}`)
}

function serverCommand(override) {
  if (override) return override
  const configuredBinary = process.env.POWERCONTEXT_GO_BINARY?.trim()
  return {
    command: configuredBinary || (process.platform === 'win32' ? 'go.exe' : 'go'),
    args: configuredBinary
      ? ['server', 'run']
      : ['run', '-tags', 'sqlite_fts5', './cmd/powercontext', 'server', 'run'],
  }
}

function spawnServer(root, env, command) {
  const selected = serverCommand(command)
  return spawn(selected.command, selected.args, {
    cwd: root,
    env,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
}

export async function startPowerContextServer(options = {}) {
  const root = defaultPowerContextRoot()
  if (!root) {
    throw new Error('Set POWERCONTEXT_ROOT to the PowerContext Go checkout that contains go.mod')
  }
  const port = await unusedPort()
  const home = mkdtempSync(join(options.tempRoot || tmpdir(), 'pc-dsh-e2e-'))
  const env = {
    ...process.env,
    POWERCONTEXT_HOME: home,
    POWERCONTEXT_SERVER_HTTP_HOST: '127.0.0.1',
    POWERCONTEXT_SERVER_HTTP_PORT: String(port),
  }
  const child = spawnServer(root, env, options.command)
  const logs = boundedLogBuffer(maxStartupLogBytes)
  child.stdout?.on('data', (chunk) => logs.append(chunk))
  child.stderr?.on('data', (chunk) => logs.append(chunk))
  const completed = childCompletion(child)
  const abort = new AbortController()
  const baseUrl = `http://127.0.0.1:${port}`
  try {
    const startup = await Promise.race([
      waitForUrl(`${baseUrl}/health/live`, options.timeoutMs, abort.signal).then(() => ({ type: 'ready' })),
      completed.then((result) => ({ type: 'exit', result })),
    ])
    if (startup.type === 'exit') throw childFailure(startup.result)
  } catch (error) {
    abort.abort(new Error('Server readiness wait was cancelled'))
    const result = await stopServerProcess(child, completed)
    rmSync(home, { recursive: true, force: true })
    throw startupFailure(error, result, logs)
  }
  return {
    baseUrl,
    home,
    root,
    async stop() {
      await stopServerProcess(child, completed)
      rmSync(home, { recursive: true, force: true })
    },
  }
}

function boundedLogBuffer(limit) {
  let contents = Buffer.alloc(0)
  let truncated = false
  return {
    append(chunk) {
      const bytes = Buffer.from(chunk)
      if (bytes.length >= limit) {
        truncated ||= contents.length > 0 || bytes.length > limit
        contents = Buffer.from(bytes.subarray(bytes.length - limit))
        return
      }
      const combinedSize = contents.length + bytes.length
      if (combinedSize > limit) {
        const discarded = combinedSize - limit
        contents = Buffer.concat([contents.subarray(discarded), bytes], limit)
        truncated = true
        return
      }
      contents = Buffer.concat([contents, bytes], combinedSize)
    },
    text() {
      const output = contents.toString('utf8')
      return truncated ? `${output}\n[startup logs truncated at ${limit} bytes]` : output
    },
  }
}

function childCompletion(child) {
  return new Promise((resolve) => {
    child.once('error', (error) => resolve({ error }))
    child.once('close', (code, signal) => resolve({ code, signal }))
  })
}

async function stopServerProcess(child, completed) {
  if (child.exitCode !== null || child.signalCode !== null) return completed
  child.kill()
  return Promise.race([completed, delay(3000)])
}

function childFailure(result) {
  if (result?.error) return new Error(`PowerContext Server failed to start: ${result.error.message}`)
  if (result?.signal) return new Error(`PowerContext Server exited with signal ${result.signal}`)
  if (result?.code !== undefined && result.code !== null) return new Error(`PowerContext Server exited with exit code ${result.code}`)
  return new Error('PowerContext Server exited before readiness')
}

function startupFailure(cause, result, logs) {
  const details = [cause instanceof Error ? cause.message : String(cause)]
  if (!(cause instanceof Error) || !cause.message.startsWith('PowerContext Server exited')) {
    details.push(childFailure(result).message)
  }
  const output = logs.text()
  if (output) details.push(`PowerContext Server startup log:\n${output}`)
  return new Error(details.join('\n'))
}

function delay(duration, signal) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, duration)
    if (!signal) return
    signal.addEventListener('abort', () => {
      clearTimeout(timer)
      reject(signal.reason)
    }, { once: true })
  })
}
