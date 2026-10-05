import test from 'node:test'
import assert from 'node:assert/strict'
import { once } from 'node:events'
import { generateTOTP, validateLoginRequest } from './login.mjs'
import { createLoginServer } from './server.mjs'

const secret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'
const state = 'flow_state_abcdefghijklmnopqrstuvwxyz123456789'
function request(overrides = {}) {
  const url = new URL('https://auth.openai.com/oauth/authorize')
  for (const [key, value] of Object.entries({client_id:'app_EMoamEEZ73f0CkXaXp7hrann',response_type:'code',redirect_uri:'http://localhost:1455/auth/callback',state,code_challenge:'a'.repeat(43),code_challenge_method:'S256'})) url.searchParams.set(key,value)
  return {authorization_url:url.href,state,email:'account@example.com',password:'upstream password',totp_secret:secret,...overrides}
}
async function endpoint(t, login) {
  const server = createLoginServer({token:'a-test-token-with-32-characters-minimum',login})
  server.listen(0,'127.0.0.1')
  await once(server,'listening')
  t.after(() => new Promise(resolve => server.close(resolve)))
  return `http://127.0.0.1:${server.address().port}/login`
}
const headers = {'content-type':'application/json',authorization:'Bearer a-test-token-with-32-characters-minimum'}

test('local TOTP follows RFC6238 and preserves leading zeroes', () => {
  assert.equal(generateTOTP(secret,59000), '287082')
  assert.equal(generateTOTP(secret,1111111109000), '081804')
  assert.equal(generateTOTP(secret.toLowerCase(),59000), '287082')
  assert.throws(() => generateTOTP('invalid!'))
})
test('login accepts only the official PKCE flow with matching state and loopback callback', () => {
  assert.equal(validateLoginRequest(request()).email,'account@example.com')
  for (const overrides of [
    {authorization_url:'https://auth.openai.com.attacker.example/oauth/authorize'},
    {authorization_url:request().authorization_url.replace('https:', 'http:')},
    {authorization_url:request().authorization_url.replace('localhost%3A1455','attacker.example%3A1455')},
    {state:'a_different_state'},
    {authorization_url:request().authorization_url.replace('S256','plain')},
    {totp_secret:'not-a-base32-seed!'},
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
test('login stops for CAPTCHA, email verification, consent and disabled accounts without filling secrets', async () => {
  for(const [kind,error_code,status] of [
    ['captcha','captcha_required','needs_attention'],['email_code','email_verification_required','needs_attention'],
    ['consent','consent_required','needs_attention'],['disabled','account_unavailable','failed'],
  ]) {
    const page=loginPage([{kind}])
    assert.deepEqual(await completeLoginPage(page,request(),new AbortController().signal),{status,error_code})
    assert.deepEqual(page.filled,[])
  }
})
test('login refuses offsite credential fields, repeated password prompts and mismatched callbacks', async () => {
  const offsite=loginPage([{kind:'password',url:'https://evil.example/'}])
  assert.equal((await completeLoginPage(offsite,request(),new AbortController().signal)).status,'needs_attention')
  assert.deepEqual(offsite.filled,[])
  const repeated=loginPage([{kind:'password'},{kind:'password'}])
  let ticks=0
  assert.equal((await completeLoginPage(repeated,request(),new AbortController().signal,{now:()=>ticks++ * 10000,wait:async()=>{}})).error_code,'login_timeout')
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
  assert.equal((await completeLoginPage(mfa,request({totp_secret:''}),new AbortController().signal)).error_code,'additional_verification_required')
  assert.deepEqual(mfa.filled,[])
})

test('slow password navigation waits for a callback without resubmitting or declaring bad credentials', async () => {
  const page=loginPage([{kind:'password'},{kind:'password'},{kind:'callback',url:`http://localhost:1455/auth/callback?code=delayed-code&state=${state}`}])
  const result=await completeLoginPage(page,request(),new AbortController().signal,{wait:page.next})
  assert.equal(result.status,'success')
  assert.equal(page.filled.length,1)
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
})
