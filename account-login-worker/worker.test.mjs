import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { generateTOTP, getLoginTOTP, safeCheckResult, safeLoginResult, validateLoginRequest } from './login.mjs'
import { createLoginServer } from './server.mjs'

const secret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
const state = 'flow_state_abcdefghijklmnopqrstuvwxyz123456789'
function request(overrides = {}) {
  const url = new URL('https://auth.openai.com/oauth/authorize')
  for (const [key, value] of Object.entries({client_id:'app_EMoamEEZ73f0CkXaXp7hrann',response_type:'code',redirect_uri:'http://localhost:1455/auth/callback',state,code_challenge:'a'.repeat(43),code_challenge_method:'S256'})) url.searchParams.set(key,value)
  return {authorization_url:url.href,state,email:'account@example.com',password:'upstream password',totp_secret:secret,...overrides}
}
async function endpoint(t, login, path = '/login') {
  const server = createLoginServer({token:'a-test-token-with-32-characters-minimum',login})
  server.listen(0,'127.0.0.1')
  await once(server,'listening')
  t.after(() => new Promise(resolve => server.close(resolve)))
  return `http://127.0.0.1:${server.address().port}${path}`
}
const headers = {'content-type':'application/json',authorization:'Bearer a-test-token-with-32-characters-minimum'}

test('local TOTP follows RFC6238 and preserves leading zeroes', () => {
  assert.equal(generateTOTP(secret,59000), '287082')
  assert.equal(generateTOTP(secret,1111111109000), '081804')
  assert.equal(generateTOTP(secret.toLowerCase(),59000), '287082')
  assert.throws(() => generateTOTP('invalid!'))
})
test('login TOTP clock corrects host drift without changing the timeout clock or trusting stale samples', async () => {
  const { LoginClock } = await import('./login.mjs')
  const serverTime = 1111111109000
  let localTime = serverTime - 107000
  const clock = new LoginClock(() => localTime)
  assert.equal(clock.now(), localTime)
  clock.observe(new Date(serverTime).toUTCString(), undefined, localTime, localTime)
  assert.equal(clock.now(), serverTime)
  assert.equal((await getLoginTOTP(request(), new AbortController().signal, { now: () => clock.now() })).code, '081804')
  assert.equal(localTime, serverTime - 107000)
  for (const [date, age, startedAt, receivedAt] of [
    ['invalid', undefined, localTime, localTime],
    [undefined, undefined, localTime, localTime],
    [new Date(59000).toUTCString(), '582', localTime, localTime],
    [new Date(59000).toUTCString(), undefined, localTime, localTime + 10000],
    [new Date(59000).toUTCString(), undefined, localTime + 1, localTime],
  ]) {
    clock.observe(date, age, startedAt, receivedAt)
    assert.equal(clock.now(), serverTime)
  }
  localTime += 30000
  assert.equal(clock.now(), serverTime + 30000)
})
test('TOTP providers are explicit and local remains the default for existing requests', () => {
  for (const provider of [undefined, '', 'local', '2fa_cn']) {
    assert.equal(validateLoginRequest(request({ totp_provider: provider })).totp_provider, provider || 'local')
  }
  for (const provider of ['https://attacker.example', 'remote', null, 1]) {
    assert.throws(() => validateLoginRequest(request({ totp_provider: provider })), /invalid_request/)
  }
})
test('workspace preference is an explicit validated server instruction', () => {
  for (const preference of [undefined, '', 'team_then_personal']) {
    assert.equal(validateLoginRequest(request({ workspace_preference: preference })).workspace_preference, preference || '')
  }
  for (const preference of [null, 1, 'first', 'personal', 'TEAM_THEN_PERSONAL']) {
    assert.throws(() => validateLoginRequest(request({ workspace_preference: preference })), /invalid_request/)
  }
})
test('local TOTP never contacts a provider and remote TOTP uses only the fixed endpoint', async () => {
  const signal = new AbortController().signal
  assert.deepEqual(await getLoginTOTP(request(), signal, { now: () => 1111111109000,
    fetch: () => assert.fail('local TOTP must not leave the worker') }), { code: '081804' })
  const result = await getLoginTOTP(request({ totp_provider: '2fa_cn', totp_secret: ` ${secret.toLowerCase()} ` }), signal, {
    fetch: async (url, options) => {
      assert.equal(url, `https://2fa.cn/codes/${secret}`)
      assert.equal(options.method, 'GET')
      assert.equal(options.redirect, 'error')
      assert.equal(options.cache, 'no-store')
      assert.equal(options.headers.Authorization, undefined)
      return Response.json({ code: '081804', ostring: secret })
    },
  })
  assert.deepEqual(result, { code: '081804' })
})
test('remote TOTP validates its bounded JSON contract without exposing provider content', async () => {
  const input = request({ totp_provider: '2fa_cn' })
  for (const response of [
    Response.json({ code: 81804, ostring: secret }),
    Response.json({ code: '81804', ostring: secret }),
    Response.json({ code: '1234567', ostring: secret }),
    Response.json({ code: '123456', ostring: 'ANOTHERSEED234567' }),
    Response.json({ code: '123456' }),
    new Response('{"code": "private"', { headers: { 'content-type': 'application/json' } }),
    new Response('private', { headers: { 'content-type': 'text/html' } }),
    new Response('x'.repeat(4097), { headers: { 'content-type': 'application/json' } }),
    new Response(null, { status: 302, headers: { location: 'https://attacker.example/private' } }),
  ]) {
    assert.deepEqual(await getLoginTOTP(input, new AbortController().signal, { fetch: async () => response }),
      { status: 'needs_attention', error_code: 'totp_provider_invalid_response' })
  }
  for (const fetch of [async () => { throw new Error('https://2fa.cn/codes/private') },
    async () => new Response('private', { status: 503 })]) {
    assert.deepEqual(await getLoginTOTP(input, new AbortController().signal, { fetch }),
      { status: 'failed', error_code: 'totp_provider_unavailable' })
  }
})
test('remote TOTP is bounded by provider timeout and caller cancellation', async () => {
  const fetch = async (_url, { signal }) => new Promise((resolve, reject) => {
    signal.addEventListener('abort', () => reject(signal.reason), { once: true })
  })
  assert.deepEqual(await getLoginTOTP(request({ totp_provider: '2fa_cn' }), new AbortController().signal,
    { fetch, providerTimeoutMs: 10 }), { status: 'failed', error_code: 'totp_provider_timeout' })
  const aborted = new AbortController()
  aborted.abort()
  assert.deepEqual(await getLoginTOTP(request({ totp_provider: '2fa_cn' }), aborted.signal,
    { fetch: () => assert.fail('cancelled job must not contact provider') }),
    { status: 'needs_attention', error_code: 'login_timeout' })
  const cancelled = new AbortController()
  assert.deepEqual(await getLoginTOTP(request({ totp_provider: '2fa_cn' }), cancelled.signal,
    { fetch: async () => { cancelled.abort(); return Response.json({ code: '081804', ostring: secret }) } }),
    { status: 'needs_attention', error_code: 'login_timeout' })
})
test('login accepts only the official PKCE flow with matching state and loopback callback', () => {
  assert.equal(validateLoginRequest(request()).email,'account@example.com')
  assert.equal(validateLoginRequest(request({ expected_account_id: '4a67072b-1cec-4df6-b925-b9b0b7b18788' })).expected_account_id,
    '4a67072b-1cec-4df6-b925-b9b0b7b18788')
  for (const overrides of [
    {authorization_url:'https://auth.openai.com.attacker.example/oauth/authorize'},
    {authorization_url:request().authorization_url.replace('https:', 'http:')},
    {authorization_url:request().authorization_url.replace('localhost%3A1455','attacker.example%3A1455')},
    {state:'a_different_state'},
    {authorization_url:request().authorization_url.replace('S256','plain')},
    {totp_secret:'not-a-base32-seed!'},
    {expected_account_id:'account id with spaces'},
    {expected_account_id:'x'.repeat(129)},
    {expected_account_id:1},
    {password:''},
    {email:'x'.repeat(321)},
  ]) assert.throws(() => validateLoginRequest(request(overrides)))
})
test('worker requires its service token and never starts an unauthorized login', async t => {
  let calls = 0
  const url = await endpoint(t,async () => {calls++;return {status:'failed',error_code:'invalid_credentials'}})
  const response = await fetch(url,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(request())})
  assert.equal(response.status,401)
  assert.equal(calls,0)
})
test('worker rejects malformed or oversized input without reflecting secrets', async t => {
  const url = await endpoint(t,async () => assert.fail('must not start'))
  for(const body of ['{malformed','x'.repeat(33000),JSON.stringify(request({authorization_url:'https://evil.example/?password=private'}))]) {
    const response = await fetch(url,{method:'POST',headers,body})
    assert.ok([400,413].includes(response.status))
    assert.doesNotMatch(await response.text(),/private|upstream password|GEZDG/)
  }
})
test('worker serializes browser jobs and masks exceptions', async t => {
  let release, started
  const ready = new Promise(resolve => {started=resolve})
  const pending = new Promise(resolve => {release=resolve})
  const url = await endpoint(t,async () => {started();await pending;throw new Error('password=private seed=private')})
  const first = fetch(url,{method:'POST',headers,body:JSON.stringify(request())})
  await ready
  const second = await fetch(url,{method:'POST',headers,body:JSON.stringify(request())})
  assert.equal(second.status,429)
  release()
  const result = await first
  assert.equal(result.status,200)
  assert.deepEqual(await result.json(),{status:'failed',error_code:'automation_unavailable'})
})
test('worker check returns only status and maps safe errors', async t => {
  let result = { status: 'success', code: 'authorization-code', state, password: 'private', totp_secret: 'private' }
  const url = await endpoint(t, async () => result, '/check')
  let response = await fetch(url, { method: 'POST', headers, body: JSON.stringify(request()) })
  assert.deepEqual(await response.json(), { status: 'success' })
  for (result of [
    { status: 'needs_attention', error_code: 'captcha_required', code: 'private', state, password: 'private' },
    { status: 'failed', error_code: 'invalid_credentials', code: 'private', state, totp_secret: 'private' },
    { status: 'success', code: 'private', state: 'wrong-state' },
  ]) {
    response = await fetch(url, { method: 'POST', headers, body: JSON.stringify(request()) })
    assert.deepEqual(await response.json(), result.status === 'success'
      ? { status: 'failed', error_code: 'invalid_callback' }
      : { status: result.status, error_code: result.error_code })
  }
})

test('worker check rejects malformed and oversized input without starting browser', async t => {
  const url = await endpoint(t, async () => assert.fail('must not start'), '/check')
  for (const body of ['{malformed', 'x'.repeat(33000), JSON.stringify(request({ authorization_url: 'https://evil.example/?password=private' }))]) {
    const response = await fetch(url, { method: 'POST', headers, body })
    assert.ok([400, 413].includes(response.status))
    assert.doesNotMatch(await response.text(), /private|GEZDG/)
  }
})

test('worker check serializes jobs and clears sensitive fields after success or exception', async t => {
  let captured
  let release
  const pending = new Promise(resolve => { release = resolve })
  const url = await endpoint(t, async payload => { captured = payload; await pending; return { status: 'success', code: 'private', state } }, '/check')
  const first = fetch(url, { method: 'POST', headers, body: JSON.stringify(request()) })
  while (!captured) await new Promise(resolve => setImmediate(resolve))
  const second = await fetch(url, { method: 'POST', headers, body: JSON.stringify(request()) })
  assert.equal(second.status, 429)
  release()
  assert.deepEqual(await (await first).json(), { status: 'success' })
  assert.equal(captured.password, '')
  assert.equal(captured.totp_secret, '')

  let failedPayload
  const failing = await endpoint(t, async payload => { failedPayload = payload; throw new Error('private password') }, '/check')
  assert.deepEqual(await (await fetch(failing, { method: 'POST', headers, body: JSON.stringify(request()) })).json(),
    { status: 'failed', error_code: 'automation_unavailable' })
  assert.equal(failedPayload.password, '')
  assert.equal(failedPayload.totp_secret, '')
})

test('worker returns only a matching authorization code or a sanitized handoff', async t => {
  let result = {status:'success',code:'authorization-code',state}
  const url = await endpoint(t,async () => result)
  let response=await fetch(url,{method:'POST',headers,body:JSON.stringify(request())})
  assert.deepEqual(await response.json(),result)
  result={status:'success',code:'private',state:'wrong-state'}
  response=await fetch(url,{method:'POST',headers,body:JSON.stringify(request())})
  assert.deepEqual(await response.json(),{status:'failed',error_code:'invalid_callback'})
  result={status:'needs_attention',error_code:'captcha_required',password:'private'}
  response=await fetch(url,{method:'POST',headers,body:JSON.stringify(request())})
  assert.deepEqual(await response.json(),{status:'needs_attention',error_code:'captcha_required'})
})

const { completeLoginPage, parseCallback } = await import('./browser.mjs')
function loginPage(steps) {
  let position = 0
  const filled = []
  return {
    filled,
    url: () => steps[position].url || 'https://auth.openai.com/log-in',
    screen: async () => steps[position],
    fillAndContinue: async (kind, value) => {filled.push([kind,value]);position++},
    next: async () => {position=Math.min(position+1,steps.length-1)},
  }
}
test('ordinary login fills email, password and locally generated authenticator code once', async () => {
  const page=loginPage([{kind:'email'},{kind:'password'},{kind:'totp'},{kind:'callback',url:`http://localhost:1455/auth/callback?code=valid-code&state=${state}`}])
  assert.deepEqual(await completeLoginPage(page,request(),new AbortController().signal,{now:()=>59000,wait:page.next}),{status:'success',code:'valid-code',state})
  assert.deepEqual(page.filled,[['email','account@example.com'],['password','upstream password'],['totp','287082']])
})
test('remote TOTP is fetched at the authenticator step only and never before a handoff', async () => {
  const page = loginPage([{ kind: 'email' }, { kind: 'password' }, { kind: 'totp' },
    { kind: 'callback', url: `http://localhost:1455/auth/callback?code=valid-code&state=${state}` }])
  let calls = 0
  const fetch = async () => {
    calls++
    assert.deepEqual(page.filled.map(([kind]) => kind), ['email', 'password'])
    return Response.json({ code: '081804', ostring: secret })
  }
  assert.equal((await completeLoginPage(page, request({ totp_provider: '2fa_cn' }), new AbortController().signal, { fetch })).status, 'success')
  assert.equal(calls, 1)
  assert.deepEqual(page.filled[2], ['totp', '081804'])
  const captcha = loginPage([{ kind: 'captcha' }])
  await completeLoginPage(captcha, request({ totp_provider: '2fa_cn' }), new AbortController().signal,
    { fetch: () => assert.fail('no provider calls before authenticator prompt') })
})
test('login stops for CAPTCHA, email verification, consent and disabled accounts without filling secrets', async () => {
  for(const [kind,error_code,status] of [
    ['captcha','captcha_required','needs_attention'],['email_code','email_verification_required','needs_attention'],
    ['consent','consent_required','needs_attention'],['disabled','account_disabled','failed'],
    ['unavailable','account_unavailable','failed'],
    ['invalid_totp','invalid_totp_code','needs_attention'],
  ]) {
    const page=loginPage([{kind}])
    assert.deepEqual(await completeLoginPage(page,request(),new AbortController().signal,
      { wait: () => assert.fail('recognized challenge must be returned immediately') }),{status,error_code})
    assert.deepEqual(page.filled,[])
  }
})

test('workspace selection is manual without a stored account and exact-match automatic on reauthentication', async () => {
  const manual = loginPage([{ kind: 'workspace' }])
  assert.deepEqual(await completeLoginPage(manual, request(), new AbortController().signal),
    { status: 'needs_attention', error_code: 'workspace_selection_required' })
  const page = loginPage([{ kind: 'workspace' },
    { kind: 'callback', url: `http://localhost:1455/auth/callback?code=workspace-code&state=${state}` }])
  const selected = []
  page.selectWorkspace = async accountID => { selected.push(accountID); await page.next() }
  assert.deepEqual(await completeLoginPage(page, request({ expected_account_id: 'account-id' }), new AbortController().signal,
    { wait: page.next }), { status: 'success', code: 'workspace-code', state })
  assert.deepEqual(selected, ['account-id'])
})
test('login refuses offsite credential fields, repeated password prompts and mismatched callbacks', async () => {
  const offsite=loginPage([{kind:'password',url:'https://evil.example/'}])
  assert.equal((await completeLoginPage(offsite,request(),new AbortController().signal)).status,'needs_attention')
  assert.deepEqual(offsite.filled,[])
  const repeated=loginPage([{kind:'password'},{kind:'password'}])
  let ticks=0
  assert.equal((await completeLoginPage(repeated,request(),new AbortController().signal,{now:()=>ticks++ * 10000,wait:async()=>{}})).error_code,'password_step_timeout')
  assert.equal(repeated.filled.length,1)
  assert.deepEqual(parseCallback(`http://localhost:1455/auth/callback?code=private&state=wrong`,state),{status:'failed',error_code:'invalid_callback'})
  assert.equal(parseCallback(`http://localhost:1455/auth/callback?code=private&state=${state}&state=other`,state).status,'failed')
})
test('login cancellation and missing authenticator secret stop without credential retries', async () => {
  const abort=new AbortController();abort.abort()
  const page=loginPage([{kind:'password'}])
  assert.equal((await completeLoginPage(page,request(),abort.signal)).error_code,'login_timeout')
  assert.deepEqual(page.filled,[])
  const mfa=loginPage([{kind:'totp'}])
  assert.equal((await completeLoginPage(mfa,request({totp_secret:''}),new AbortController().signal)).error_code,'totp_secret_required')
  assert.deepEqual(mfa.filled,[])
})

test('slow password navigation waits for a callback without resubmitting or declaring bad credentials', async () => {
  const page=loginPage([{kind:'password'},{kind:'password'},{kind:'callback',url:`http://localhost:1455/auth/callback?code=delayed-code&state=${state}`}])
  const result=await completeLoginPage(page,request(),new AbortController().signal,{wait:page.next})
  assert.equal(result.status,'success')
  assert.equal(page.filled.length,1)
})
test('login reports safe stage failures and timeouts without retrying credentials', async () => {
  for (const kind of ['email', 'password', 'totp']) {
    const page = loginPage([{ kind }])
    page.fillAndContinue = async () => { throw new Error('private password seed URL') }
    assert.deepEqual(await completeLoginPage(page, request(), new AbortController().signal),
      { status: 'needs_attention', error_code: `${kind}_submission_failed` })
    const stalled = loginPage([{ kind }, { kind }])
    let time = 0
    assert.deepEqual(await completeLoginPage(stalled, request(), new AbortController().signal,
      { now: () => time, deadline: 3, wait: async () => { time++ } }),
      { status: 'needs_attention', error_code: `${kind}_step_timeout` })
    assert.equal(stalled.filled.length, 1)
  }
  let time = 0
  const unknown = loginPage([{ kind: 'loading' }])
  assert.deepEqual(await completeLoginPage(unknown, request(), new AbortController().signal,
    { now: () => time, deadline: 3, wait: async () => { time++ } }),
    { status: 'needs_attention', error_code: 'login_screen_unrecognized' })
})
test('navigation failures survive initial blank pages and classification retries a page transition', async () => {
  for (const error_code of ['login_navigation_blocked', 'login_navigation_failed']) {
    const page = loginPage([{ kind: 'navigation_error', error_code, url: 'about:blank' }])
    assert.deepEqual(await completeLoginPage(page, request(), new AbortController().signal),
      { status: 'needs_attention', error_code })
  }
  const page = loginPage([{ kind: 'loading' },
    { kind: 'callback', url: `http://localhost:1455/auth/callback?code=valid-code&state=${state}` }])
  page.screen = async () => { throw new Error('Execution context was destroyed, most likely because of a navigation.') }
  assert.equal((await completeLoginPage(page, request(), new AbortController().signal, { wait: page.next })).status, 'success')
})
test('diagnostic output is a fixed code and never includes worker or provider secrets', () => {
  for (const error_code of ['login_navigation_blocked', 'login_navigation_failed', 'login_screen_unrecognized',
    'email_submission_failed', 'password_submission_failed', 'totp_submission_failed',
    'email_step_timeout', 'password_step_timeout', 'totp_step_timeout', 'totp_secret_required',
    'totp_provider_invalid_response', 'totp_provider_unavailable', 'totp_provider_timeout', 'invalid_totp_code']) {
    assert.deepEqual(safeLoginResult({ status: 'needs_attention', error_code, message: secret, password: 'private' }, state),
      { status: 'needs_attention', error_code })
  }
})
test('worker clears transient credentials after success and after exceptions', async t => {
  let captured, shouldThrow = false
  const url = await endpoint(t, async input => {
    captured = input
    assert.equal(input.totp_provider, '2fa_cn')
    if (shouldThrow) throw new Error('private')
    return { status: 'success', code: 'valid-code', state }
  })
  for (shouldThrow of [false, true]) {
    const response = await fetch(url, { method: 'POST', headers, body: JSON.stringify(request({ totp_provider: '2fa_cn' })) })
    await response.json()
    assert.equal(captured.password, '')
    assert.equal(captured.totp_secret, '')
  }
})

const { handleLoginRequest } = await import('./browser.mjs')
function routedRequest(url, status, location, method = 'GET') {
  const effects = { fetched: [], fulfilled: [], navigated: [], aborted: false, blocked: false, callback: null }
  const request = {url:()=>url,method:()=>method,isNavigationRequest:()=>true,frame:()=> 'main'}
  const route = {
    request:()=>request,
    fetch:async options=>{effects.fetched.push(options);return {status:()=>status,headers:()=>({location})}},
    fulfill:async value=>{effects.fulfilled.push(value)},
    abort:async()=>{effects.aborted=true},
  }
  const context={state,mainFrame:'main',deadline:Date.now()+150000,redirects:0,
    navigate:async value=>effects.navigated.push(value),
    setCallback:value=>{effects.callback=value},setBlocked:()=>{effects.blocked=true}}
  return {route,context,effects}
}
test('HTTP redirects are checked before follow and localhost callback is captured without a network request',async()=>{
  const fixture=routedRequest('https://auth.openai.com/authorize',302,`http://localhost:1455/auth/callback?code=valid-code&state=${state}`)
  await handleLoginRequest(fixture.route,fixture.context)
  assert.equal(fixture.effects.fetched.length,1)
  assert.equal(fixture.effects.fetched[0].maxRedirects,0)
  assert.equal(fixture.effects.callback.code,'valid-code')
  assert.equal(fixture.effects.fulfilled[0].status,200)
  assert.deepEqual(fixture.effects.navigated,[])
})
test('credential POST redirects never replay to an external host or repeat a password POST',async()=>{
  for(const [status,location] of [[307,'https://evil.example/'],[308,'https://auth.openai.com/repeat'],[302,'https://evil.example/']]) {
    const fixture=routedRequest('https://auth.openai.com/log-in/password',status,location,'POST')
    await handleLoginRequest(fixture.route,fixture.context)
    assert.equal(fixture.effects.fetched.length,1)
    assert.equal(fixture.effects.aborted,true)
    assert.equal(fixture.effects.blocked,true)
    assert.deepEqual(fixture.effects.navigated,[])
  }
})
test('validated same-origin redirects become fresh GET navigations with a bounded redirect count',async()=>{
  const fixture=routedRequest('https://auth.openai.com/log-in',303,'/next','POST')
  await handleLoginRequest(fixture.route,fixture.context)
  assert.deepEqual(fixture.effects.navigated,['https://auth.openai.com/next'])
  assert.equal(fixture.effects.fulfilled[0].status,200)
  const loop=routedRequest('https://auth.openai.com/log-in',302,'/next')
  loop.context.redirects=12
  await handleLoginRequest(loop.route,loop.context)
  assert.equal(loop.effects.aborted,true)
  assert.deepEqual(loop.effects.navigated,[])
})

test('subresource redirects cannot carry authorization headers to another allowed origin',async()=>{
  const fixture=routedRequest('https://auth.openai.com/account',302,'https://cdn.auth0.com/asset')
  fixture.route.request=()=>({url:()=> 'https://auth.openai.com/account',method:()=> 'GET',isNavigationRequest:()=>false,frame:()=> 'main'})
  await handleLoginRequest(fixture.route,fixture.context)
  assert.equal(fixture.effects.fetched.length,1)
  assert.equal(fixture.effects.aborted,true)
  assert.equal(fixture.effects.blocked,false)
})

test('blocked third-party assets do not prevent ordinary credential login', async () => {
  const fixture = routedRequest('https://telemetry.example/pixel', 200)
  fixture.route.request = () => ({ url: () => 'https://telemetry.example/pixel', method: () => 'GET',
    isNavigationRequest: () => false, frame: () => 'main' })
  await handleLoginRequest(fixture.route, fixture.context)
  assert.equal(fixture.effects.aborted, true)
  assert.equal(fixture.effects.fetched.length, 0)
  assert.equal(fixture.effects.blocked, false)
})

test('official Sentinel assets and its exact SDK exchange load without allowing credential forms', async () => {
  for (const [url, method, navigation, allowed, resourceType = 'script'] of [
    ['https://sentinel.openai.com/backend-api/sentinel/sdk.js', 'GET', false, true],
    ['https://sentinel.openai.com/sentinel/fixture/sdk.js', 'GET', false, true],
    ['https://sentinel.openai.com/backend-api/sentinel/sdk.js', 'POST', false, false],
    ['https://sentinel.openai.com/backend-api/sentinel/sdk.js', 'GET', true, false],
    ['https://sentinel.openai.com.evil.example/sdk.js', 'GET', false, false],
    ['https://sentinel.openai.com:8443/sdk.js', 'GET', false, false],
    ['https://sentinel.openai.com/backend-api/sentinel/req', 'POST', false, true, 'fetch'],
    ['https://sentinel.openai.com/backend-api/sentinel/req', 'POST', false, true, 'xhr'],
    ['https://sentinel.openai.com/backend-api/sentinel/req', 'POST', true, false, 'document'],
    ['https://sentinel.openai.com/backend-api/sentinel/req', 'POST', false, false, 'document'],
    ['https://sentinel.openai.com/backend-api/sentinel/req', 'PUT', false, false, 'fetch'],
    ['https://sentinel.openai.com/backend-api/sentinel/req?password=secret', 'POST', false, false, 'fetch'],
    ['https://sentinel.openai.com/backend-api/sentinel/other', 'POST', false, false, 'fetch'],
    ['https://sentinel.openai.com.evil.example/backend-api/sentinel/req', 'POST', false, false, 'fetch'],
    ['https://sentinel.openai.com:8443/backend-api/sentinel/req', 'POST', false, false, 'fetch'],
  ]) {
    const fixture = routedRequest(url, 200, undefined, method)
    fixture.route.request = () => ({ url: () => url, method: () => method,
      isNavigationRequest: () => navigation, frame: () => 'main', resourceType: () => resourceType })
    await handleLoginRequest(fixture.route, fixture.context)
    assert.equal(fixture.effects.fetched.length, allowed ? 1 : 0, `${method} ${url}`)
    assert.equal(fixture.effects.aborted, !allowed)
  }
  for (const [status, location] of [[307, 'https://sentinel.openai.com/backend-api/sentinel/req'],
    [302, 'https://auth.openai.com/log-in'], [302, 'https://evil.example/']]) {
    const url = 'https://sentinel.openai.com/backend-api/sentinel/req'
    const fixture = routedRequest(url, status, location, 'POST')
    fixture.route.request = () => ({ url: () => url, method: () => 'POST',
      isNavigationRequest: () => false, frame: () => 'main', resourceType: () => 'fetch' })
    await handleLoginRequest(fixture.route, fixture.context)
    assert.equal(fixture.effects.fetched.length, 1)
    assert.equal(fixture.effects.fetched[0].maxRedirects, 0)
    assert.equal(fixture.effects.aborted, true)
  }
})

test('asset redirects do not consume the login navigation redirect budget', async () => {
  const fixture = routedRequest('https://auth.openai.com/asset', 302, '/asset-final')
  fixture.context.redirects = 12
  fixture.route.request = () => ({ url: () => 'https://auth.openai.com/asset', method: () => 'GET',
    isNavigationRequest: () => false, frame: () => 'main' })
  fixture.route.fetch = async options => {
    fixture.effects.fetched.push(options)
    return { status: () => fixture.effects.fetched.length === 1 ? 302 : 200,
      headers: () => ({ location: '/asset-final' }) }
  }
  await handleLoginRequest(fixture.route, fixture.context)
  assert.equal(fixture.context.redirects, 12)
  assert.equal(fixture.effects.aborted, false)
  assert.equal(fixture.effects.fulfilled.length, 1)
})

test('a failed main navigation produces a safe navigation error while failed assets remain optional', async () => {
  for (const mainNavigation of [true, false]) {
    const fixture = routedRequest('https://auth.openai.com/fixture', 200)
    fixture.route.request = () => ({ url: () => 'https://auth.openai.com/fixture', method: () => 'GET',
      isNavigationRequest: () => mainNavigation, frame: () => 'main' })
    fixture.route.fetch = async () => { throw new Error('upstream password private URL') }
    const errors = []
    fixture.context.setBlocked = code => errors.push(code)
    await handleLoginRequest(fixture.route, fixture.context)
    assert.equal(fixture.effects.aborted, true)
    assert.deepEqual(errors, mainNavigation ? ['login_navigation_failed'] : [])
  }
})

test('only successful official login navigations can calibrate the TOTP clock', async () => {
  for (const [url, status, navigation, expected] of [
    ['https://auth.openai.com/log-in', 200, true, 1],
    ['https://auth.openai.com/log-in', 302, true, 1],
    ['https://auth.openai.com/log-in', 403, true, 0],
    ['https://auth.openai.com/asset.js', 200, false, 0],
    ['https://sentinel.openai.com/backend-api/sentinel/sdk.js', 200, false, 0],
    ['https://evil.example/log-in', 200, true, 0],
  ]) {
    const fixture = routedRequest(url, status)
    fixture.route.request = () => ({ url: () => url, method: () => 'GET',
      isNavigationRequest: () => navigation, frame: () => 'main' })
    const samples = []
    fixture.context.clock = { localNow: () => 100, observe: (...sample) => samples.push(sample) }
    fixture.route.fetch = async () => ({ status: () => status,
      headers: () => ({ date: 'Fri, 18 Mar 2005 01:58:29 GMT', age: '0' }) })
    await handleLoginRequest(fixture.route, fixture.context)
    assert.deepEqual(samples, expected ? [['Fri, 18 Mar 2005 01:58:29 GMT', '0', 100, 100]] : [])
  }
})

test('login network diagnostics identify failures without disclosing URLs or credentials', async () => {
  for (const [url, method, status, destination, event] of [
    ['https://sentinel.openai.com/private?code=private-code', 'POST', 200, 'sentinel.openai.com', 'request_blocked'],
    ['https://private-secret.example/private?password=private-password', 'GET', 200, 'other', 'request_blocked'],
    ['https://auth.openai.com/private?state=private-state', 'POST', 403, 'auth.openai.com', 'http_error'],
  ]) {
    const fixture = routedRequest(url, status, undefined, method)
    fixture.route.request = () => ({ url: () => url, method: () => method,
      isNavigationRequest: () => false, frame: () => 'main' })
    const events = []
    fixture.context.onDiagnostic = value => events.push(value)
    await handleLoginRequest(fixture.route, fixture.context)
    assert.deepEqual(events, [{ event, destination, method, navigation: false,
      ...(event === 'http_error' ? { status } : {}) }])
    assert.doesNotMatch(JSON.stringify(events), /private|password|code=|state=/)
  }
  const fixture = routedRequest('https://auth.openai.com/private?secret=private', 200)
  fixture.route.fetch = async () => { throw new Error('private password provider response') }
  const events = []
  fixture.context.onDiagnostic = value => events.push(value)
  await handleLoginRequest(fixture.route, fixture.context)
  assert.deepEqual(events, [{ event: 'request_failed', destination: 'auth.openai.com', method: 'GET', navigation: true }])
})

test('login stage diagnostics report transitions only and exclude form values', async () => {
  const page = loginPage([{ kind: 'password' }, { kind: 'password' }, { kind: 'totp' },
    { kind: 'callback', url: `http://localhost:1455/auth/callback?code=private-code&state=${state}` }])
  const events = []
  const result = await completeLoginPage(page, request(), new AbortController().signal,
    { wait: page.next, onDiagnostic: value => events.push(value) })
  assert.equal(result.status, 'success')
  assert.deepEqual(events, [{ event: 'screen', kind: 'password' }, { event: 'screen', kind: 'totp' }])
  assert.doesNotMatch(JSON.stringify(events), /fixture-password|private-code|state|GEZDG/)
})


test('one-time checks require both password and TOTP challenges before success', async () => {
  for (const [steps, expected] of [
    [[{kind:'email'}, {kind:'password'}, {kind:'totp'}], {status:'success', code:'valid-code', state}],
    [[{kind:'email'}, {kind:'password'}], {status:'needs_attention', error_code:'totp_not_challenged'}],
    [[], {status:'needs_attention', error_code:'password_not_challenged'}],
  ]) {
    const page = loginPage([...steps, {kind:'callback', url:`http://localhost:1455/auth/callback?code=valid-code&state=${state}`}])
    assert.deepEqual(await completeLoginPage(page, request(), new AbortController().signal, { checkOnly: true, wait: page.next }), expected)
  }
})

test('one-time checks finish before workspace or consent only with accepted challenges from this flow', async () => {
  for (const kind of ['workspace', 'consent']) {
    for (const [checkOnly, verified, steps] of [
      [true, true, ['password', 'totp']], [true, false, ['password', 'totp']],
      [true, true, ['password']], [false, true, ['password', 'totp']],
    ]) {
      const page = loginPage([...steps.map(kind => ({ kind })), { kind }])
      page.credentialsVerified = () => verified
      page.selectWorkspace = () => assert.fail('check must not choose a workspace')
      const result = await completeLoginPage(page, request(), new AbortController().signal, { checkOnly, wait: page.next })
      assert.deepEqual(result, checkOnly && verified && steps.length === 2
        ? { status: 'credentials_verified', state }
        : { status: 'needs_attention', error_code: kind === 'workspace' ? 'workspace_selection_required' : 'consent_required' })
    }
  }
  assert.deepEqual(safeCheckResult({ status: 'credentials_verified', state, secret: 'private' }, state), { status: 'success' })
  assert.deepEqual(safeCheckResult({ status: 'credentials_verified', state: 'wrong-state' }, state), { status: 'failed', error_code: 'invalid_callback' })
  assert.deepEqual(safeLoginResult({ status: 'credentials_verified', state }, state), { status: 'failed', error_code: 'automation_unavailable' })
  for (const preference of [{ expected_account_id: 'saved-workspace' }, { workspace_preference: 'team_then_personal' }]) {
    const page = loginPage([{ kind: 'password' }, { kind: 'totp' }, { kind: 'workspace' }])
    page.credentialsVerified = () => false
    let selections = 0
    page.selectWorkspace = async () => { selections++ }
    let ticks = 0
    const result = await completeLoginPage(page, request(preference), new AbortController().signal,
      { checkOnly: true, now: () => ticks++ * 10000, wait: page.next })
    assert.equal(selections, 0)
    assert.deepEqual(result, { status: 'needs_attention', error_code: 'workspace_selection_required' })
  }
})

test('check evidence requires ordered semantic acceptance from the exact official verification APIs', async () => {
  const password = { continue_url: 'https://auth.openai.com/mfa', page: { type: 'mfa_challenge' } }
  const mfa = { continue_url: 'https://auth.openai.com/sign-in-with-chatgpt/codex/consent', page: { type: 'sign_in_with_chatgpt_codex_consent' } }
  for (const [reply, status, origin, frame, expected] of [
    [mfa, 200, 'https://auth.openai.com', 'main', true],
    [{ ...mfa, error: { code: 'invalid_code' } }, 200, 'https://auth.openai.com', 'main', false],
    [{ ...mfa, success: false }, 200, 'https://auth.openai.com', 'main', false],
    [{ ...mfa, page: { type: 'mfa_challenge' } }, 200, 'https://auth.openai.com', 'main', false],
    [{ ...mfa, continue_url: 'https://evil.example/consent' }, 200, 'https://auth.openai.com', 'main', false],
    [mfa, 403, 'https://auth.openai.com', 'main', false],
    [mfa, 200, 'https://sentinel.openai.com', 'main', false],
    [mfa, 200, 'https://auth.openai.com', 'child', false],
    ['invalid JSON', 200, 'https://auth.openai.com', 'main', false],
  ]) {
    const fixture = routedRequest('https://auth.openai.com/api/accounts/password/verify', 200)
    fixture.context.checkVerification = { password: false, totp: false }
    for (const [path, body, code, host, sourceFrame] of [
      ['/api/accounts/mfa/verify', mfa, 200, 'https://auth.openai.com', 'main'],
      ['/api/accounts/password/verify', password, 200, 'https://auth.openai.com', 'main'],
      ['/api/accounts/mfa/verify', reply, status, origin, frame],
    ]) {
      fixture.route.request = () => ({ url: () => host + path, method: () => 'POST',
        isNavigationRequest: () => false, frame: () => sourceFrame, resourceType: () => 'fetch' })
      fixture.route.fetch = async () => ({ status: () => code, headers: () => ({ 'content-type': 'application/json' }),
        body: async () => Buffer.from(typeof body === 'string' ? body : JSON.stringify(body)) })
      await handleLoginRequest(fixture.route, fixture.context)
      if (!fixture.context.checkVerification.password) assert.equal(fixture.context.checkVerification.totp, false)
    }
    assert.deepEqual(fixture.context.checkVerification, { password: true, totp: expected })
  }
})

test('check endpoint always requests challenge verification and local TOTP', async t => {
  const url = await endpoint(t, async (payload, signal, options) => {
    assert.equal(options.checkOnly, true)
    assert.equal(payload.totp_provider, 'local')
    return {status:'needs_attention', error_code:'totp_not_challenged'}
  }, '/check')
  const response = await fetch(url, {method:'POST', headers, body: JSON.stringify(request({totp_provider:'2fa_cn'}))})
  assert.deepEqual(await response.json(), {status:'needs_attention', error_code:'totp_not_challenged'})
})

test('probe is authenticated, opts into model authorization, and keeps callback data on that route', async t => {
  const calls = []
  const url = await endpoint(t, async (payload, signal, options) => {
    calls.push({ ...payload, options })
    return { status: 'success', code: 'probe-code', state, password: 'private', access_token: 'private' }
  }, '/probe')
  const unauthorized = await fetch(url, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(request()) })
  assert.equal(unauthorized.status, 401)
  assert.equal(calls.length, 0)
  for (const path of ['/probe', '/check', '/login']) {
    const response = await fetch(new URL(path, url), { method: 'POST', headers,
      body: JSON.stringify(request({ totp_provider: '2fa_cn' })) })
    assert.equal(response.status, 200)
    assert.deepEqual(await response.json(), path === '/check' ? { status: 'success' }
      : { status: 'success', code: 'probe-code', state })
    assert.equal(calls.at(-1).totp_provider, path === '/login' ? '2fa_cn' : 'local')
    assert.equal(calls.at(-1).options.checkOnly, path === '/check')
    assert.equal(Boolean(calls.at(-1).options.probe), path === '/probe')
    assert.equal(Boolean(calls.at(-1).options.autoConsent), path === '/login')
    assert.equal(calls.at(-1).workspace_preference, path === '/probe' ? 'team_then_personal' : '')
  }
})

test('probe sanitizes failures and rejects mismatched or malformed callback codes', async t => {
  let result
  const url = await endpoint(t, async () => result, '/probe')
  for (const [input, expected] of [
    [{ status: 'failed', error_code: 'account_disabled' }, { status: 'failed', error_code: 'account_disabled' }],
    [{ status: 'failed', error_code: 'account_unavailable' }, { status: 'failed', error_code: 'account_unavailable' }],
    [{ status: 'success', code: 'private', state: 'wrong-state' }, { status: 'failed', error_code: 'invalid_callback' }],
    [{ status: 'success', code: 'private\ncode', state }, { status: 'failed', error_code: 'invalid_callback' }],
    [{ status: 'credentials_verified', state }, { status: 'failed', error_code: 'automation_unavailable' }],
    [{ status: 'failed', error_code: 'private detail' }, { status: 'failed', error_code: 'automation_unavailable' }],
  ]) {
    result = { ...input, password: 'private', totp_secret: secret }
    const response = await fetch(url, { method: 'POST', headers, body: JSON.stringify(request()) })
    assert.equal(response.status, 200)
    assert.deepEqual(await response.json(), expected)
  }
})

test('probe client cancellation aborts its browser and clears credentials before another job', async t => {
  let captured, markStarted, markFinished
  const started = new Promise(resolve => { markStarted = resolve })
  const finished = new Promise(resolve => { markFinished = resolve })
  const url = await endpoint(t, async (payload, signal) => {
    captured = payload
    markStarted()
    await new Promise(resolve => signal.addEventListener('abort', resolve, { once: true }))
    markFinished()
    return { status: 'needs_attention', error_code: 'login_timeout' }
  }, '/probe')
  const abort = new AbortController()
  const pending = fetch(url, { method: 'POST', headers, body: JSON.stringify(request()), signal: abort.signal })
  // A missing route must fail rather than leave this regression waiting forever.
  await Promise.race([started, pending.then(response => assert.equal(response.status, 200))])
  abort.abort()
  await assert.rejects(pending, { name: 'AbortError' })
  await finished
  await new Promise(resolve => setImmediate(resolve))
  assert.equal(captured.password, '')
  assert.equal(captured.totp_secret, '')
})

test('probe callback requires completed password and local TOTP submissions from this flow', async () => {
  for (const [steps, expected] of [
    [['password', 'totp'], { status: 'success', code: 'probe-code', state }],
    [['password'], { status: 'needs_attention', error_code: 'totp_not_challenged' }],
    [['totp'], { status: 'needs_attention', error_code: 'password_not_challenged' }],
    [[], { status: 'needs_attention', error_code: 'password_not_challenged' }],
  ]) {
    const page = loginPage([...steps.map(kind => ({ kind })),
      { kind: 'callback', url: `http://localhost:1455/auth/callback?code=probe-code&state=${state}` }])
    assert.deepEqual(await completeLoginPage(page, request({ totp_provider: '2fa_cn' }), new AbortController().signal,
      { probe: true, now: () => 59000, wait: page.next, fetch: () => assert.fail('probe TOTP must remain local') }), expected)
    if (steps.includes('totp')) assert.deepEqual(page.filled.at(-1), ['totp', '287082'])
  }
  const page = loginPage([{ kind: 'password' }, { kind: 'totp' }])
  const fill = page.fillAndContinue
  page.fillAndContinue = async (kind, value) => {
    if (kind === 'password') return fill(kind, value)
    page.url = () => `http://localhost:1455/auth/callback?code=probe-code&state=${state}`
    throw new Error('submission failed before completing MFA')
  }
  assert.deepEqual(await completeLoginPage(page, request(), new AbortController().signal, { probe: true }),
    { status: 'needs_attention', error_code: 'totp_not_challenged' })
})

test('probe never treats a consent screen as login-only success and requires both submissions before approval', async () => {
  for (const [steps, expected] of [
    [[], 'password_not_challenged'], [['password'], 'totp_not_challenged'],
  ]) {
    const page = loginPage([...steps.map(kind => ({ kind })), { kind: 'consent' }])
    page.credentialsVerified = () => true
    page.approveConsent = () => assert.fail('incomplete credential proof must not grant consent')
    assert.deepEqual(await completeLoginPage(page, request(), new AbortController().signal, { probe: true }),
      { status: 'needs_attention', error_code: expected })
  }
  for (const kind of ['captcha', 'email_code', 'additional']) {
    const page = loginPage([{ kind: 'password' }, { kind: 'totp' }, { kind }])
    page.approveConsent = () => assert.fail('security challenges are never consent')
    assert.equal((await completeLoginPage(page, request(), new AbortController().signal, { probe: true })).error_code,
      { captcha: 'captcha_required', email_code: 'email_verification_required', additional: 'additional_verification_required' }[kind])
  }
})

test('login and probe can approve the exact Codex consent form after password and MFA, while checks cannot', async () => {
  for (const options of [{ autoConsent: true }, { probe: true }, { checkOnly: true }, {}, { probe: true, checkOnly: true }]) {
    const page = loginPage([{ kind: 'password' }, { kind: 'totp' }, { kind: 'consent' },
      { kind: 'callback', url: `http://localhost:1455/auth/callback?code=probe-code&state=${state}` }])
    let approved = 0
    page.approveConsent = async () => { approved++; await page.next() }
    const result = await completeLoginPage(page, request(), new AbortController().signal, options)
    const expectedApproval = (options.autoConsent || options.probe) && !options.checkOnly
    assert.deepEqual(result, expectedApproval ? { status: 'success', code: 'probe-code', state }
      : { status: 'needs_attention', error_code: 'consent_required' })
    assert.equal(approved, expectedApproval ? 1 : 0)
  }
  for (const kind of ['workspace', 'consent']) {
    const abort = new AbortController()
    const page = loginPage([{ kind: 'password' }, { kind: 'totp' }, { kind }])
    const screen = page.screen
    page.screen = async () => {
      const result = await screen()
      if (result.kind === kind) abort.abort()
      return result
    }
    page.approveConsent = () => assert.fail('cancelled probe must not approve')
    page.selectWorkspace = () => assert.fail('cancelled probe must not select a workspace')
    assert.deepEqual(await completeLoginPage(page, request({ workspace_preference: 'team_then_personal' }), abort.signal, { probe: true }),
      { status: 'needs_attention', error_code: 'login_timeout' })
  }
})
