import test from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import net from 'node:net'
import { once } from 'node:events'
import { existsSync } from 'node:fs'
import { chromium } from 'playwright'
import { browserLogin, classifyLoginScreen, completeLoginPage, createIsolatedLoginContext, handleLoginRequest } from './browser.mjs'

const state = 'browser_fixture_state_abcdefghijklmnopqrstuvwxyz'

test('production browser login completes email, password and TOTP despite blocked optional resources', async t => {
  const seed = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
  const submissions = []
  const server = http.createServer(async (req, res) => {
    const path = new URL(req.url, 'http://fixture').pathname
    if (req.method === 'POST') {
      let body = ''
      for await (const chunk of req) body += chunk
      submissions.push([path, Object.fromEntries(new URLSearchParams(body))])
      if (path === '/email') res.writeHead(303, { location: '/password', 'set-cookie': 'fixture_session=present; HttpOnly; Secure; SameSite=Lax; Path=/' }).end()
      else if (path === '/password') res.writeHead(302, { location: '/totp' }).end()
      else if (path === '/verify') res.writeHead(302, { location: `http://localhost:1455/auth/callback?code=fixture-authorization&state=${state}` }).end()
      else res.writeHead(404).end()
      return
    }
    res.setHeader('content-type', 'text/html')
    if (path === '/oauth/authorize') {
      res.end('<h1>Sign in</h1><img src="https://telemetry.example/pixel"><form method="post" action="/email"><input type="email" name="email"><button>Continue</button></form>')
    } else if (path === '/password') {
      assert.match(req.headers.cookie || '', /fixture_session=present/)
      res.end('<h1>Enter your password</h1><form method="post" action="/password"><input name="password" type="password"><button>Continue</button></form>')
    } else if (path === '/totp') {
      assert.match(req.headers.cookie || '', /fixture_session=present/)
      res.end('<h1>Enter your authenticator code</h1><form method="post" action="/verify"><input name="code" autocomplete="one-time-code"><button>Verify</button></form><a>Send a code to your email instead</a>')
    } else res.writeHead(404).end()
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const fixtureOrigin = `http://127.0.0.1:${server.address().port}`
  for (const provider of ['local', '2fa_cn']) {
    let blockedAssets = 0, providerCalls = 0
    const navigations = []
    const result = await browserLogin({ authorization_url: `https://auth.openai.com/oauth/authorize?state=${state}`,
      state, email: 'fixture@example.com', password: 'fixture-password', totp_secret: seed, totp_provider: provider },
    new AbortController().signal, {
      now: () => 59000,
      fetch: async url => {
        providerCalls++
        assert.equal(url, `https://2fa.cn/codes/${seed}`)
        assert.equal(submissions.at(-1)[0], '/password')
        return Response.json({ code: '081804', ostring: seed })
      },
      launch: async options => {
        const browser = await chromium.launch({ ...options,
          ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
        t.after(() => browser.close())
        return {
          close: () => browser.close(),
          newContext: async options => {
            const context = await browser.newContext(options)
            context.on('page', page => {
              page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations.push(frame.url()) })
              page.on('requestfailed', request => navigations.push([request.url(), request.failure()?.errorText]))
            })
            const registerRoute = context.route.bind(context)
            // Keep real browser URLs and DOM on the official origin. Translate
            // only the HTTP transport to loopback; never contact live accounts.
            context.route = (pattern, handler) => registerRoute(pattern, route => handler({
              request: () => route.request(),
              fetch: async options => {
                const url = new URL(options.url)
                assert.equal(url.origin, 'https://auth.openai.com')
                const response = await route.fetch({ ...options, url: fixtureOrigin + url.pathname + url.search,
                  headers: await route.request().allHeaders() })
                // APIRequestContext normally stores upstream cookies on the
                // official origin. Map that same transport side effect back.
                const cookies = (await context.cookies()).filter(cookie => cookie.domain === '127.0.0.1')
                if (cookies.length) await context.addCookies(cookies.map(cookie => ({ ...cookie, domain: 'auth.openai.com' })))
                return response
              },
              fulfill: options => route.fulfill(options),
              abort: () => {
                if (route.request().url().startsWith('https://telemetry.example/')) blockedAssets++
                return route.abort()
              },
            }))
            return context
          },
        }
      },
    })
    assert.deepEqual(result, { status: 'success', code: 'fixture-authorization', state }, JSON.stringify({ submissions, navigations }))
    assert.equal(blockedAssets, 1)
    assert.equal(providerCalls, provider === '2fa_cn' ? 1 : 0)
    assert.deepEqual(submissions.splice(0), [
      ['/email', { email: 'fixture@example.com' }], ['/password', { password: 'fixture-password' }],
      ['/verify', { code: provider === '2fa_cn' ? '081804' : '287082' }],
    ])
  }
})

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
  for (const [fixture, kind] of [
    ['<h1>Choose a workspace</h1><button>Personal</button><button>Continue</button>', 'additional'],
    ['<h1>Select an account</h1><p>Personal account</p><button>Continue</button>', 'additional'],
    ['<h1>Enter your authenticator code</h1><input name="code"><a>Send a code to your email instead</a>', 'totp'],
    ['<h1>Check your email</h1><input name="code"><a>Use your authenticator instead</a>', 'email_code'],
    ['<h1>Enter your authenticator code</h1><input name="code"><div role="alert">Incorrect code</div>', 'invalid_totp'],
    ['<h1>Enter your authenticator code</h1><input name="code"><div role="alert">Invalid authentication code</div>', 'invalid_totp'],
    ['<h1>Check your email</h1><input name="code"><div role="alert">Incorrect code</div><a>Use your authenticator instead</a>', 'email_code'],
  ]) {
    html = fixture
    const path = fixture.includes('workspace') ? '/workspace' : fixture.includes('Select an account') ? '/account-select' : '/mfa'
    await tab.goto(`https://auth.openai.com${path}`)
    await tab.getByRole('heading').first().waitFor()
    assert.deepEqual(await classifyLoginScreen(tab), { kind })
  }
})
