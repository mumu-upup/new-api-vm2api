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
  let sentinelExchanges = 0
  let checkBeforeConsent = false
  const server = http.createServer(async (req, res) => {
    const path = new URL(req.url, 'http://fixture').pathname
    res.setHeader('Date', new Date(1111111109000).toUTCString())
    if (path === '/backend-api/sentinel/req' && req.method === 'POST') {
      let body = ''
      for await (const chunk of req) body += chunk
      assert.equal(body, 'fixture-sdk-request')
      sentinelExchanges++
      res.writeHead(200, { 'access-control-allow-origin': 'https://auth.openai.com', 'content-type': 'application/json' })
      res.end('{"ready":true}')
      return
    }
    if (req.method === 'POST') {
      let body = ''
      for await (const chunk of req) body += chunk
      submissions.push([path, Object.fromEntries(new URLSearchParams(body))])
      if (path === '/api/accounts/password/verify' || path === '/api/accounts/mfa/verify') {
        res.setHeader('content-type', 'application/json')
        const password = path === '/api/accounts/password/verify'
        res.end(JSON.stringify({ continue_url: `https://auth.openai.com${password ? '/totp' : '/sign-in-with-chatgpt/codex/consent'}`,
          page: { type: password ? 'mfa_challenge' : 'sign_in_with_chatgpt_codex_consent' } }))
      } else if (path === '/email') res.writeHead(303, { location: '/password', 'set-cookie': 'fixture_session=present; HttpOnly; Secure; SameSite=Lax; Path=/' }).end()
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
      const submit = checkBeforeConsent
        ? 'fetch(\'/api/accounts/password/verify\', {method:\'POST\', body:new URLSearchParams(new FormData(this))}).then(r => r.json()).then(next => location.href = next.continue_url)'
        : 'this.submit()'
      res.end(`<h1>Enter your password</h1><script src="https://sentinel.openai.com/backend-api/sentinel/sdk.js"></script><form method="post" action="/password" onsubmit="event.preventDefault(); window.fixtureSentinel.then(() => ${submit})"><input name="password" type="password"><button>Continue</button></form>`)
    } else if (path === '/backend-api/sentinel/sdk.js') {
      res.setHeader('content-type', 'application/javascript')
      res.end('window.fixtureSentinel = fetch("https://sentinel.openai.com/backend-api/sentinel/req", {method:"POST", body:"fixture-sdk-request"}).then(r => { if (!r.ok) throw new Error("SDK unavailable") })')
    } else if (path === '/totp') {
      assert.match(req.headers.cookie || '', /fixture_session=present/)
      const submit = checkBeforeConsent
        ? 'onsubmit="event.preventDefault(); fetch(\'/api/accounts/mfa/verify\', {method:\'POST\', body:new URLSearchParams(new FormData(this))}).then(r => r.json()).then(next => location.href = next.continue_url)"'
        : ''
      res.end(`<h1>Enter your authenticator code</h1><form method="post" action="/verify" ${submit}><input name="code" autocomplete="one-time-code"><button>Verify</button></form><a>Send a code to your email instead</a>`)
    } else if (path === '/sign-in-with-chatgpt/codex/consent') {
      res.end('<h1>Choose a workspace</h1><form method="post" action="/must-not-authorize"><input type="radio" name="workspace_id" value="fixture"><button>Continue</button></form>')
    } else res.writeHead(404).end()
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const fixtureOrigin = `http://127.0.0.1:${server.address().port}`
  for (const [provider, clockSkewMs, beforeConsent] of [['local', 0, false], ['2fa_cn', 0, false], ['local', -107000, true]]) {
    checkBeforeConsent = beforeConsent
    let blockedAssets = 0, providerCalls = 0, sentinelAssets = 0
    const navigations = []
    const result = await browserLogin({ authorization_url: `https://auth.openai.com/oauth/authorize?state=${state}`,
      state, email: 'fixture@example.com', password: 'fixture-password', totp_secret: seed, totp_provider: provider },
    AbortSignal.timeout(15000), {
      checkOnly: provider === 'local',
      now: () => 1111111109000 + clockSkewMs,
      fetch: async url => {
        providerCalls++
        assert.equal(url, `https://2fa.cn/codes/${seed}`)
        assert.equal(submissions.at(-1)[0], '/password')
        return Response.json({ code: '287082', ostring: seed })
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
                if (url.origin === 'https://sentinel.openai.com') {
                  if (url.pathname === '/backend-api/sentinel/sdk.js') {
                    assert.equal(route.request().method(), 'GET')
                    sentinelAssets++
                  } else {
                    assert.equal(url.pathname, '/backend-api/sentinel/req')
                    assert.equal(route.request().method(), 'POST')
                  }
                } else assert.equal(url.origin, 'https://auth.openai.com')
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
    assert.deepEqual(result, beforeConsent ? { status: 'credentials_verified', state }
      : { status: 'success', code: 'fixture-authorization', state }, JSON.stringify({ submissions, navigations }))
    assert.equal(blockedAssets, 1)
    assert.equal(sentinelAssets, 1)
    assert.equal(sentinelExchanges, 1)
    sentinelExchanges = 0
    assert.equal(providerCalls, provider === '2fa_cn' ? 1 : 0)
    assert.deepEqual(submissions.splice(0), [
      ['/email', { email: 'fixture@example.com' }], [beforeConsent ? '/api/accounts/password/verify' : '/password', { password: 'fixture-password' }],
      [beforeConsent ? '/api/accounts/mfa/verify' : '/verify', { code: provider === '2fa_cn' ? '287082' : '081804' }],
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
  await context.route('**/*', route => route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body: html }))
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
    ['<h1>查看你的身份验证器应用</h1><input name="code" autocomplete="one-time-code"><div role="alert">验证码不正确。请重试。</div>', 'invalid_totp'],
    ['<h1>Check your email</h1><input name="code"><div role="alert">Incorrect code</div><a>Use your authenticator instead</a>', 'email_code'],
    ['<h1>Your account has been deactivated</h1>', 'disabled'],
    ['<h1>Your account is disabled</h1>', 'disabled'],
    ['<h1>Your account is temporarily unavailable</h1>', 'unavailable'],
    ['<h1>Your account has been deleted</h1>', 'unavailable'],
  ]) {
    html = fixture
    const path = fixture.includes('workspace') ? '/workspace' : fixture.includes('Select an account') ? '/account-select' : '/mfa'
    await tab.goto(`https://auth.openai.com${path}`)
    await tab.getByRole('heading').first().waitFor()
    assert.deepEqual(await classifyLoginScreen(tab), { kind })
    if (kind === 'invalid_totp') {
      assert.deepEqual(await completeLoginPage({
        url: () => tab.url(), screen: () => classifyLoginScreen(tab),
        fillAndContinue: () => assert.fail('rejected OTP must not be resubmitted'),
      }, { state }, new AbortController().signal),
      { status: 'needs_attention', error_code: 'invalid_totp_code' })
    }
  }
  html = '<h1>选择一个工作空间</h1><form method="post" action="/workspace"><label><input type="radio" name="workspace_id" value="workspace-id">我的工作空间</label><button>继续</button></form>'
  await tab.goto('https://auth.openai.com/workspace')
  assert.deepEqual(await classifyLoginScreen(tab), { kind: 'workspace' })
})


test('production workspace selection prefers a unique team, falls back to personal, and preserves saved bindings', async t => {
  const radio = (id, label, attrs = '') => `<label><input type="radio" name="workspace_id" value="${id}" ${attrs}>${label}</label>`
  const personal = radio('personal-id', '个人账户', 'checked')
  const team = radio('team-id', 'My Team')
  const submissions = []
  let html = ''
  const server = http.createServer(async (req, res) => {
    if (req.method === 'POST') {
      let body = ''
      for await (const chunk of req) body += chunk
      submissions.push(Object.fromEntries(new URLSearchParams(body)))
      res.writeHead(302, { location: `http://localhost:1455/auth/callback?code=workspace-code&state=${state}` }).end()
    } else {
      res.setHeader('content-type', 'text/html; charset=utf-8')
      res.end(html)
    }
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const fixtureOrigin = `http://127.0.0.1:${server.address().port}`
  for (const tc of [
    { name: 'team over checked personal', choices: personal + team, selected: 'team-id' },
    { name: 'custom organization name', choices: personal + radio('org-id', 'Example Studio'), selected: 'org-id' },
    { name: 'single team', choices: team, selected: 'team-id' },
    { name: 'only personal Chinese', choices: personal, selected: 'personal-id' },
    { name: 'only personal English', choices: radio('personal-id', 'Personal account'), selected: 'personal-id' },
    { name: 'personal avatar initials before label', choices: radio('personal-id', 'Pa<br>Personal account') + team, selected: 'team-id' },
    { name: 'different initials cannot identify a personal workspace', choices: radio('unknown-id', 'XX<br>Personal account') + team },
    { name: 'workspace name mentioning personal stays ambiguous', choices: radio('unknown-id', 'My Team<br>Personal account') + team },
    { name: 'extra personal label text stays ambiguous', choices: radio('unknown-id', 'Pa<br>Personal account<br>Example Studio') + team },
    { name: 'ambiguous teams', choices: personal + team + radio('other-team', 'Second Team') },
    { name: 'ambiguous personal', choices: personal + radio('other-personal', 'Personal account') },
    { name: 'unreadable alternative', choices: personal + radio('unknown', '') },
    { name: 'no preference remains manual', choices: personal + team, input: { workspace_preference: '' } },
    { name: 'saved personal wins over preference', choices: personal + team, input: { expected_account_id: 'personal-id' }, selected: 'personal-id' },
    { name: 'saved missing cannot fall back', choices: personal + team, input: { expected_account_id: 'missing-id' } },
    { name: 'duplicate saved id', choices: team + radio('team-id', 'Second Team'), input: { expected_account_id: 'team-id' } },
    { name: 'duplicate preferred id', choices: personal + radio('personal-id', 'My Team') },
    { name: 'disabled team cannot force personal fallback', choices: personal + radio('disabled-id', 'My Team', 'disabled') },
    { name: 'offsite form', choices: personal + team, action: 'https://evil.example/workspace', error: 'login_navigation_blocked' },
    { name: 'GET form', choices: personal + team, method: 'get' },
    { name: 'submit override', choices: personal + team, submit: 'formaction="https://evil.example/workspace"', error: 'login_navigation_blocked' },
    { name: 'new window', choices: personal + team, target: '_blank', error: 'login_navigation_blocked' },
  ]) {
    await t.test(tc.name, async () => {
      submissions.length = 0
      html = `<h1>选择一个工作空间</h1><form method="${tc.method || 'post'}" action="${tc.action || '/workspace'}" target="${tc.target || '_self'}">${tc.choices}<button ${tc.submit || ''}>继续</button></form>`
      const result = await browserLogin({ authorization_url: `https://auth.openai.com/oauth/authorize?state=${state}`,
        state, workspace_preference: 'team_then_personal', ...tc.input }, new AbortController().signal, {
        onDiagnostic: () => {},
        launch: async options => {
          const browser = await chromium.launch({ ...options,
            ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
          return {
            close: () => browser.close(),
            newContext: async options => {
              const context = await browser.newContext(options)
              const registerRoute = context.route.bind(context)
              context.route = (pattern, handler) => registerRoute(pattern, route => handler({
                request: () => route.request(),
                fetch: async options => {
                  const url = new URL(options.url)
                  assert.equal(url.origin, 'https://auth.openai.com')
                  return route.fetch({ ...options, url: fixtureOrigin + url.pathname + url.search })
                },
                fulfill: options => route.fulfill(options), abort: () => route.abort(),
              }))
              return context
            },
          }
        },
      })
      assert.deepEqual(result, tc.selected ? { status: 'success', code: 'workspace-code', state }
        : { status: 'needs_attention', error_code: tc.error || 'workspace_selection_required' })
      assert.deepEqual(submissions, tc.selected ? [{ workspace_id: tc.selected }] : [])
    })
  }
})

test('login and probe authorize only the official Codex consent form after both credential submissions', async t => {
  const consentPath = '/sign-in-with-chatgpt/codex/consent'
  const passwordForm = '<h1>Enter your password</h1><form method="post" action="/password"><input name="password" type="password"><button>Continue</button></form>'
  const totpForm = '<h1>Enter your authenticator code</h1><form method="post" action="/verify"><input name="code" autocomplete="one-time-code"><button>Verify</button></form>'
  const workspaceChoices = '<label><input type="radio" name="workspace_id" value="personal">Personal account</label><label><input type="radio" name="workspace_id" value="team">Example Studio</label>'
  let scenario, submissions
  const server = http.createServer(async (req, res) => {
    if (req.method === 'POST') {
      let body = ''
      for await (const chunk of req) body += chunk
      submissions.push([req.url, Object.fromEntries(new URLSearchParams(body))])
      const location = req.url === '/password' && !scenario.skipMFA ? '/totp'
        : ['/password', '/verify'].includes(req.url) ? scenario.path || consentPath
        : `http://localhost:1455/auth/callback?code=probe-code&state=${scenario.wrongState ? 'wrong-state' : state}`
      res.writeHead(303, { location }).end()
      return
    }
    res.setHeader('content-type', 'text/html; charset=utf-8')
    if (req.url.startsWith('/oauth/authorize')) res.end(scenario.skipPassword ? totpForm : passwordForm)
    else if (req.url === '/totp') res.end(totpForm)
    else res.end(`<h1>Sign in to Codex</h1><form method="${scenario.method || 'post'}" action="${scenario.action || consentPath}" target="${scenario.target || '_self'}">${scenario.workspace ? scenario.choices || workspaceChoices : ''}${scenario.fields || ''}${scenario.buttons ?? '<button type="submit">Continue</button>'}</form>${scenario.extraForm || ''}`)
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const fixtureOrigin = `http://127.0.0.1:${server.address().port}`
  const browser = await chromium.launch({ headless: true, chromiumSandbox: true,
    ...(existsSync('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome') ? { channel: 'chrome' } : {}) })
  t.after(() => browser.close())
  for (const tc of [
    { name: 'observed combined workspace consent', workspace: true, approved: true },
    { name: 'single preselected personal workspace is confirmed without enabling its radio', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="personal" disabled checked>Personal account</label>',
      approved: true, submittedWorkspace: {} },
    { name: 'single preselected team workspace is confirmed without enabling its radio', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      approved: true, submittedWorkspace: {} },
    { name: 'preselected workspace preserves a matching saved binding', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      input: { expected_account_id: 'team' }, options: { autoConsent: true }, approved: true, submittedWorkspace: {} },
    { name: 'preselected workspace never replaces a different saved binding', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      input: { expected_account_id: 'saved-other-team' }, options: { autoConsent: true }, error: 'workspace_selection_required' },
    { name: 'unselected disabled workspace still requires a person', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled>Example Studio</label>', error: 'workspace_selection_required' },
    { name: 'disabled selected team among multiple workspaces still requires a person', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label><label><input type="radio" name="workspace_id" value="personal">Personal account</label>', error: 'workspace_selection_required' },
    { name: 'preselected workspace rejects a conflicting hidden workspace value', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      fields: '<input type="hidden" name="workspace_id" value="other-team">', error: 'workspace_selection_required' },
    { name: 'preselected workspace preserves the matching hidden workspace value', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      fields: '<input type="hidden" name="workspace_id" value="team">', approved: true },
    { name: 'preselected workspace rejects duplicate submitted workspace values', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      fields: '<input type="hidden" name="workspace_id" value="team"><input type="hidden" name="workspace_id" value="team">', error: 'workspace_selection_required' },
    { name: 'preselected workspace rejects a conflicting submitter workspace value', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      buttons: '<button type="submit" name="workspace_id" value="other-team">Continue</button>', error: 'workspace_selection_required' },
    { name: 'preselected workspace cannot confirm a different official action', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      action: '/other', error: 'login_navigation_blocked' },
    { name: 'preselected workspace cannot submit an unknown challenge', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      fields: '<input name="security_answer">', error: 'workspace_selection_required' },
    { name: 'preselected workspace cannot use a disabled Continue button', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team" disabled checked>Example Studio</label>',
      buttons: '<button type="submit" disabled>Continue</button>', error: 'workspace_selection_required' },
    { name: 'observed personal avatar label keeps team preference', workspace: true,
      choices: '<fieldset><label><input type="radio" name="workspace_id" value="team">My Team</label><label><input type="radio" name="workspace_id" value="personal"><span>Pa</span><div>Personal account</div></label></fieldset>', approved: true },
    { name: 'personal avatar lookalike remains ambiguous', workspace: true,
      choices: '<label><input type="radio" name="workspace_id" value="team">My Team</label><label><input type="radio" name="workspace_id" value="personal"><span>XX</span><div>Personal account</div></label>', error: 'workspace_selection_required' },
    { name: 'plain consent with the same official form contract', approved: true },
    { name: 'combined consent wrong action path', workspace: true, action: '/other', error: 'login_navigation_blocked' },
    { name: 'combined consent wrong action origin', workspace: true, action: 'https://evil.example/consent', error: 'login_navigation_blocked' },
    { name: 'combined consent wrong page path', workspace: true, path: '/other-consent', error: 'login_navigation_blocked' },
    { name: 'combined consent non-submit button', workspace: true, buttons: '<button type="button">Continue</button>', error: 'workspace_selection_required' },
    { name: 'combined consent unknown input', workspace: true, fields: '<input name="security_answer">', error: 'workspace_selection_required' },
    { name: 'combined consent extra form', workspace: true, extraForm: '<form method="post"><button type="submit">Continue</button></form>', error: 'workspace_selection_required' },
    { name: 'combined consent ARIA-disabled submitter', workspace: true, buttons: '<button type="submit" aria-disabled="true">Continue</button>', error: 'workspace_selection_required' },
    { name: 'combined consent inherited disabled submitter', workspace: true, buttons: '<fieldset disabled><button type="submit">Continue</button></fieldset>', error: 'workspace_selection_required' },
    { name: 'plain consent wrong action path', action: '/other', error: 'login_navigation_blocked' },
    { name: 'plain consent wrong action origin', action: 'https://evil.example/consent', error: 'login_navigation_blocked' },
    { name: 'plain consent action user info', action: 'https://user@auth.openai.com' + consentPath, error: 'login_navigation_blocked' },
    { name: 'plain consent GET form', method: 'get' },
    { name: 'plain consent disabled button', buttons: '<button type="submit" disabled>Continue</button>' },
    { name: 'plain consent ARIA-disabled button', buttons: '<button type="submit" aria-disabled="true">Continue</button>' },
    { name: 'plain consent inherited disabled button', buttons: '<fieldset disabled><button type="submit">Continue</button></fieldset>' },
    { name: 'plain consent hidden button', buttons: '<button type="submit" hidden>Continue</button>' },
    { name: 'plain consent non-submit button', buttons: '<button type="button">Continue</button>' },
    { name: 'plain consent different label', buttons: '<button type="submit">Allow</button>' },
    { name: 'plain consent duplicate intended button', buttons: '<button type="submit">Continue</button><button type="submit">Continue</button>' },
    { name: 'plain consent overridden action', buttons: '<button type="submit" formaction="/other">Continue</button>', error: 'login_navigation_blocked' },
    { name: 'plain consent new window', target: '_blank', error: 'login_navigation_blocked' },
    { name: 'plain consent unknown required input', fields: '<input name="security_answer" required>' },
    { name: 'plain consent extra form', extraForm: '<form method="post"><button type="submit">Continue</button></form>' },
    { name: 'missing password submission', skipPassword: true, error: 'password_not_challenged' },
    { name: 'missing MFA submission', skipMFA: true, error: 'totp_not_challenged' },
    { name: 'wrong callback state', wrongState: true, approved: true, error: 'invalid_callback' },
    { name: 'normal login auto-approves the exact consent form', options: { autoConsent: true }, approved: true },
    { name: 'normal login follows a flow without an MFA challenge', skipMFA: true, options: { autoConsent: true }, approved: true },
    { name: 'normal login accepts Chinese consent', buttons: '<button type="submit">继续</button>', options: { autoConsent: true }, approved: true },
    { name: 'normal login rejects combined consent with an unknown input', workspace: true, fields: '<input name="security_answer">', options: { autoConsent: true }, error: 'workspace_selection_required' },
    { name: 'normal login rejects combined consent with wrong action', workspace: true, action: '/other', options: { autoConsent: true }, error: 'login_navigation_blocked' },
    { name: 'login cannot approve consent', options: {}, error: 'consent_required' },
    { name: 'check cannot approve consent', options: { checkOnly: true }, error: 'consent_required' },
    { name: 'check cannot submit combined consent', workspace: true, options: { checkOnly: true }, error: 'workspace_selection_required' },
  ]) {
    await t.test(tc.name, async () => {
      scenario = tc
      submissions = []
      let context
      const result = await browserLogin({ authorization_url: `https://auth.openai.com/oauth/authorize?state=${state}`,
        state, email: 'fixture@example.com', password: 'fixture-password',
        totp_secret: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', workspace_preference: 'team_then_personal', ...tc.input }, AbortSignal.timeout(15000), {
        ...(tc.options ?? { probe: true }), onDiagnostic: () => {},
        launch: async () => ({
          close: async () => { await context?.close() },
          newContext: async options => {
            context = await browser.newContext(options)
            const registerRoute = context.route.bind(context)
            context.route = (pattern, handler) => registerRoute(pattern, route => handler({
              request: () => route.request(),
              fetch: async options => {
                const url = new URL(options.url)
                assert.equal(url.origin, 'https://auth.openai.com')
                return route.fetch({ ...options, url: fixtureOrigin + url.pathname + url.search })
              },
              fulfill: options => route.fulfill(options), abort: () => route.abort(),
            }))
            return context
          },
        }),
      })
      assert.deepEqual(result, tc.approved && !tc.error ? { status: 'success', code: 'probe-code', state }
        : { status: tc.error === 'invalid_callback' ? 'failed' : 'needs_attention', error_code: tc.error || 'consent_required' })
      assert.deepEqual(submissions.filter(([path]) => !['/password', '/verify'].includes(path)), tc.approved
        ? [[consentPath, tc.workspace ? tc.submittedWorkspace ?? { workspace_id: 'team' } : {}]] : [])
    })
  }
})
