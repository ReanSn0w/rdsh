// Run inside an isolated container containing the selected published DSH package.
// Never prints the temporary launch token, cookies, or DSH's raw output.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { networkInterfaces } from 'node:os';
import { request } from 'node:http';
import { randomBytes } from 'node:crypto';
import { setTimeout as delay } from 'node:timers/promises';

const host = Object.values(networkInterfaces()).flat().find(x => x.family === 'IPv4' && !x.internal)?.address;
assert.ok(host, 'container IPv4 address required');
const authority = 'rdsh-probe.example';
const bin = process.env.DSH_PROBE_BIN || '/home/node/node_modules/@deepseek-ai/dsh/lib/bin.js';
let child;
async function stop() {
  if (!child || child.exitCode !== null) return;
  const exited = new Promise(resolve => child.once('exit', resolve));
  process.kill(-child.pid, 'SIGTERM');
  await Promise.race([exited, delay(10000).then(() => {
    if (child.exitCode === null) process.kill(-child.pid, 'SIGKILL');
  })]);
  await exited;
}
async function start() {
  let output = '';
  child = spawn(process.execPath, [bin, 'web', '--no-open', '--host', host, '--port', '3080', '--public-url', `https://${authority}/`, '--trusted-host', authority], {
    detached: true, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const collect = chunk => { output = (output + chunk.toString()).slice(-131072); };
  child.stdout.on('data', collect);
  child.stderr.on('data', collect);
  for (let i = 0; i < 180; i++) {
    const match = output.match(/dsh web: (https?:\/\/\S+)/);
    if (match) {
      const token = new URL(match[1]).searchParams.get('token');
      assert.ok(/^[A-Za-z0-9_-]{43}$/.test(token ?? ''), 'valid launch token format required');
      return token;
    }
    if (child.exitCode !== null) {
      // Only classify the failure; never dump upstream output which may hold credentials.
      const categories = ['EACCES', 'ENOENT', 'MODULE_NOT_FOUND', 'sandbox', 'bubblewrap', 'Error'];
      throw new Error(`DSH exited ${child.exitCode}; categories: ${categories.filter(s => output.includes(s)).join(',')}`);
    }
    await delay(1000);
  }
  throw new Error('DSH startup timeout');
}
function http(path, cookie) {
  return new Promise((resolve, reject) => {
    const req = request({host, port: 3080, path, headers: {host: authority, ...(cookie ? {cookie} : {})}}, res => {
      res.resume();
      res.once('end', () => resolve({status: res.statusCode, headers: res.headers}));
    });
    req.setTimeout(10000, () => req.destroy(new Error('HTTP timeout')));
    req.on('error', reject);
    req.end();
  });
}
function websocket(cookie) {
  return new Promise((resolve, reject) => {
    const req = request({host, port: 3080, path: '/api/remote.mux', headers: {
      host: authority, origin: `https://${authority}`, cookie,
      connection: 'Upgrade', upgrade: 'websocket', 'sec-websocket-version': '13',
      'sec-websocket-key': randomBytes(16).toString('base64'),
    }});
    req.once('upgrade', (res, socket) => { socket.destroy(); resolve(res.statusCode); });
    req.once('response', res => { res.resume(); resolve(res.statusCode); });
    req.on('error', reject);
    req.setTimeout(10000, () => req.destroy(new Error('WebSocket timeout')));
    req.end();
  });
}
try {
  const first = await start();
  assert.equal((await http('/')).status, 401);
  const exchanged = await http(`/?token=${encodeURIComponent(first)}`);
  assert.equal(exchanged.status, 303);
  const cookie = exchanged.headers['set-cookie']?.find(s => s.startsWith('dsh-auth-'))?.split(';')[0];
  assert.ok(cookie);
  assert.equal((await http('/', cookie)).status, 200);
  assert.equal((await http('/api/__rdsh_nonexistent')).status, 401);
  assert.equal((await http('/api/__rdsh_nonexistent', cookie)).status, 404);
  assert.equal(await websocket(cookie), 101);
  console.log('PASS: token exchange, root, HTTP API and WebSocket authentication');
  await stop();
  const second = await start();
  assert.ok(first !== second, 'launch token must rotate');
  assert.equal((await http(`/?token=${encodeURIComponent(first)}`)).status, 401);
  assert.equal((await http('/', cookie)).status, 200);
  assert.equal((await http(`/?token=${encodeURIComponent(second)}`)).status, 303);
  console.log('PASS: restart rotates launch token and preserves browser session');
} finally {
  await stop();
}
