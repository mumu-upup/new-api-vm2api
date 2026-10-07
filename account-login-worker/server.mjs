import http from 'node:http'
import { timingSafeEqual } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'
import { validateLoginRequest, safeLoginResult, safeCheckResult } from './login.mjs'

export function createLoginServer({ token, login }) {
  if (typeof token !== 'string' || token.length < 32 || /\s/.test(token)) throw new Error('invalid_worker_token')
  const expected = Buffer.from(`Bearer ${token}`)
  let busy = false
  const server = http.createServer(async (req, res) => {
    res.setHeader('Content-Type', 'application/json')
    res.setHeader('Cache-Control', 'no-store')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    if (req.method === 'GET' && req.url === '/health') {
      res.end('{"status":"ok"}')
      return
    }
    const received = Buffer.from(req.headers.authorization || '')
    if (received.length !== expected.length || !timingSafeEqual(received, expected)) {
      res.writeHead(401).end('{"error_code":"unauthorized"}')
      req.resume()
      return
    }
    if (req.method !== 'POST' || !['/login', '/check', '/probe'].includes(req.url)) {
      res.writeHead(404).end('{"error_code":"not_found"}')
      req.resume()
      return
    }
    if (busy) {
      res.writeHead(429).end('{"error_code":"worker_busy"}')
      req.resume()
      return
    }
    if (req.headers['content-type']?.split(';')[0].trim() !== 'application/json') {
      res.writeHead(400).end('{"error_code":"invalid_request"}')
      req.resume()
      return
    }
    let payload
    try {
      const chunks = []
      let size = 0
      for await (const chunk of req) {
        size += chunk.length
        if (size > 32768) {
          res.writeHead(413).end('{"error_code":"body_too_large"}')
          return
        }
        chunks.push(chunk)
      }
      payload = validateLoginRequest(JSON.parse(Buffer.concat(chunks).toString('utf8')))
    } catch {
      if (!res.headersSent) res.writeHead(400).end('{"error_code":"invalid_request"}')
      return
    }
    // Two requests can finish reading their bodies at the same time.
    if (busy) {
      res.writeHead(429).end('{"error_code":"worker_busy"}')
      return
    }
    busy = true
    const abort = new AbortController()
    const deadline = setTimeout(() => abort.abort(), 165000)
    res.on('close', () => { if (!res.writableEnded) abort.abort() })
    try {
      const checkOnly = req.url === '/check'
      const probe = req.url === '/probe'
      if (checkOnly || probe) payload.totp_provider = 'local'
      if (probe) payload.workspace_preference = 'team_then_personal'
      const result = await login(payload, abort.signal, { checkOnly, probe })
      const response = req.url === '/check' ? safeCheckResult(result, payload.state) : safeLoginResult(result, payload.state)
      res.end(JSON.stringify(response))
    } catch {
      res.end('{"status":"failed","error_code":"automation_unavailable"}')
    } finally {
      clearTimeout(deadline)
      payload.password = ''
      payload.totp_secret = ''
      busy = false
    }
  })
  server.requestTimeout = 10000
  server.headersTimeout = 5000
  server.keepAliveTimeout = 1000
  return server
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const tokenFile = process.env.GPT_ACCOUNT_WORKER_TOKEN_FILE
  if (!tokenFile) throw new Error('worker_token_file_required')
  const token = (await readFile(tokenFile, 'utf8')).trim()
  const { browserLogin } = await import('./browser.mjs')
  const server = createLoginServer({ token, login: browserLogin })
  server.listen(Number(process.env.PORT || 8787), process.env.HOST || '127.0.0.1')
  process.on('SIGTERM', () => server.close(() => process.exit(0)))
}
