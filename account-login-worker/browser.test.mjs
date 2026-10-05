import test from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import net from 'node:net'
import { once } from 'node:events'
import { existsSync } from 'node:fs'
import { chromium } from 'playwright'
import { classifyLoginScreen, completeLoginPage, createIsolatedLoginContext, handleLoginRequest } from './browser.mjs'

const state = 'browser_fixture_state_abcdefghijklmnopqrstuvwxyz'

test('real browser keeps the login cookie across redirects and intercepts the final OAuth callback', async t => {
  let passwordSubmissions = 0, mfaSubmissions = 0
  const server = http.createServer(async (req, res) => {
    if (req.url === '/start') {
      res.setHeader('content-type', 'text/html')
      res.end('<form method="post" action="/password"><input name="password" type="password"><button>Continue</button></form>')
    } else if (req.url === '/password' && req.method === 'POST') {
      for await (const chunk of req) void chunk
      passwordSubmissions++
      res.writeHead(302, { location: '/totp', 'set-cookie': 'fixture_session=present; HttpOnly; SameSite=Lax; Path=/' }).end()
    } else if (req.url === '/totp' && req.method === 'GET') {
      assert.match(req.headers.cookie || '', /fixture_session=present/)
      res.setHeader('content-type', 'text/html')
      res.end('<form method="post" action="/verify">Authenticator<input name="code" autocomplete="one-time-code"><button>Verify</button></form>')
    } else if (req.url === '/verify' && req.method === 'POST') {
      for await (const chunk of req) void chunk
      mfaSubmissions++
      res.writeHead(302, { location: `http://localhost:1455/auth/callback?code=fixture-authorization&state=${state}` }).end()
    } else {
      res.writeHead(404).end()
    }
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const fixtureOrigin = `http://127.0.0.1:${server.address().port}`
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true,
    ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
  t.after(() => browser.close())
  const context = await createIsolatedLoginContext(browser)
  const page = await context.newPage()
  let callback, blocked = false
  const guard = {
    state, mainFrame: page.mainFrame(), deadline: Date.now() + 20000, redirects: 0,
    setCallback: result => { callback = result }, setBlocked: () => { blocked = true },
    navigate: async (url, from) => {
      await page.waitForURL(from.replace('https://auth.openai.com', fixtureOrigin), { waitUntil: 'domcontentloaded' })
      await page.goto(url.replace('https://auth.openai.com', fixtureOrigin), { waitUntil: 'domcontentloaded' })
    },
  }
  // The fake upstream remains on loopback. Only its URL is translated at the
  // network boundary so the production allowlist stays fixed to OpenAI.
  await context.route('**/*', async route => {
    const request = route.request()
    await handleLoginRequest({
      request: () => ({ url: () => request.url().replace(fixtureOrigin, 'https://auth.openai.com'),
        method: () => request.method(), isNavigationRequest: () => request.isNavigationRequest(), frame: () => request.frame() }),
      fetch: options => route.fetch({ ...options, url: options.url.replace('https://auth.openai.com', fixtureOrigin) }),
      fulfill: options => route.fulfill(options), abort: () => route.abort(),
    }, guard)
  })
  await page.goto(`${fixtureOrigin}/start`)
  await page.locator('input[name="password"]').fill('fixture-password')
  await page.getByRole('button', { name: 'Continue' }).click()
  await page.locator('input[name="code"]').fill('123456')
  await page.getByRole('button', { name: 'Verify' }).click()
  await page.getByText('Authorization received.', { exact: true }).waitFor()
  assert.deepEqual(callback, { status: 'success', code: 'fixture-authorization', state })
  assert.equal(blocked, false)
  assert.equal(passwordSubmissions, 1)
  assert.equal(mfaSubmissions, 1)
})

test('login pages cannot open WebSockets outside the guarded HTTP flow', async t => {
  let connections = 0
  const server = net.createServer(socket => { connections++; socket.destroy() })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => new Promise(resolve => server.close(resolve)))
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true,
    ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
  t.after(() => browser.close())
  const context = await createIsolatedLoginContext(browser)
  await context.route('**/*', route => route.fulfill({ status: 200, contentType: 'text/html', body: 'fixture' }))
  const page = await context.newPage()
  await page.goto('https://auth.openai.com/fixture')
  await page.evaluate(port => new Promise(resolve => {
    const socket = new WebSocket(`wss://127.0.0.1:${port}/fixture`)
    socket.onerror = () => resolve()
    socket.onclose = () => resolve()
  }), server.address().port)
  assert.equal(connections, 0)
})

test('actual login DOM hands password reset and authenticator enrollment to a person without filling fields', async t => {
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true,
    ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
  t.after(() => browser.close())
  const context = await createIsolatedLoginContext(browser)
  const tab = await context.newPage()
  let html = ''
  await context.route('**/*', route => route.fulfill({ status: 200, contentType: 'text/html', body: html }))
  for (const [path, fixture] of [
    ['/log-in/password', '<h1>Choose a new password</h1><input type="password" autocomplete="new-password"><button>Continue</button>'],
    ['/log-in/password', '<h1>Change your password</h1><input type="password"><input type="password"><button>Continue</button>'],
    ['/reset-password', '<h1>Password</h1><input type="password"><button>Submit</button>'],
    ['/mfa/enroll', '<h1>Set up an authenticator</h1><input name="code" autocomplete="one-time-code"><button>Verify</button>'],
  ]) {
    html = fixture
    await tab.goto(`https://auth.openai.com${path}`)
    const page = { url: () => tab.url(), screen: () => classifyLoginScreen(tab),
      fillAndContinue: () => assert.fail(`credentials must not be filled into ${path}`) }
    assert.deepEqual(await completeLoginPage(page, { state, email: 'fixture@example.com', password: 'fixture-password', totp_secret: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ' }, new AbortController().signal),
      { status: 'needs_attention', error_code: 'additional_verification_required' })
  }
  html = '<h1>Enter your password</h1><input type="password" autocomplete="current-password"><a>Reset password</a><button>Continue</button>'
  await tab.goto('https://auth.openai.com/log-in/password')
  assert.deepEqual(await classifyLoginScreen(tab), { kind: 'password' })
  html = '<h1>Enter your authenticator code</h1><input name="code" autocomplete="one-time-code"><button>Verify</button>'
  await tab.goto('https://auth.openai.com/mfa')
  assert.deepEqual(await classifyLoginScreen(tab), { kind: 'totp' })
})
