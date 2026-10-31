import { spawn } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { access, rm } from 'node:fs/promises';
import { constants } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { prepareHome } from './home.mjs';
import { ensureInstalled } from './install.mjs';
import { atomicJSON, bindAddress, launchToken, publicOrigin, serverReady } from './protocol.mjs';

const handoff = process.env.DSH_HANDOFF_FILE || '/run/rdsh/connection.json';
const health = '/tmp/rdsh-health.json';
let stopping = false, child, killTimer;
const installAbort = new AbortController();
function stop() {
  stopping = true;
  installAbort.abort();
  if (child?.pid && child.exitCode === null) {
    try { process.kill(-child.pid, 'SIGTERM'); } catch {}
    killTimer ??= setTimeout(() => { try { process.kill(-child.pid, 'SIGKILL'); } catch {} }, 8000);
    killTimer.unref();
  }
}
process.on('SIGTERM', stop);
process.on('SIGINT', stop);

async function main() {
  process.umask(0o077);
  const origin = publicOrigin(process.env.PUBLIC_URL);
  const host = bindAddress(undefined, process.env.DSH_BIND_INTERFACE);
  const port = 3080;
  await access(dirname(handoff), constants.W_OK | constants.X_OK);
  await Promise.all([handoff, health].map(path => rm(path, { force:true })));
  const paths = await prepareHome(process.env.HOME);
  const env = { ...process.env, HOME:process.env.HOME, DSH_HOME:paths.dshHome, NARB_NATIVE_CACHE_DIR:paths.nativeCache, npm_config_cache:paths.npmCache, NO_COLOR:'1', FORCE_COLOR:'0' };
  const installation = await ensureInstalled({version:process.env.DSH_VERSION, manifests:join(dirname(fileURLToPath(import.meta.url)), 'releases'), paths, env, signal:installAbort.signal});
  if (stopping) return;
  const generation = randomUUID();
  child = spawn(process.execPath, [join(installation,'node_modules/@deepseek-ai/dsh/lib/bin.js'), 'web','--no-open','--host',host,'--port',String(port),'--public-url',origin.href,'--trusted-host',origin.host], {
    cwd:paths.workspace, env, detached:true, stdio:['ignore','pipe','pipe'],
  });
  let pending='', announced=false, publication=Promise.resolve();
  const failures=new Set();
  const classify=chunk=> {
    const text=chunk.toString('utf8');
    for(const [pattern,label] of [[/MODULE_NOT_FOUND|ERR_MODULE_NOT_FOUND|native binding/,'dependency'],[/EACCES|EPERM/,'permissions'],[/SANDBOX_UNAVAILABLE/,'sandbox'],[/ENOSPC/,'disk-space'],[/ECONNREFUSED|ENOTFOUND|ETIMEDOUT/,'network']]) {
      if(pattern.test(text)) failures.add(label);
    }
  };
  // Upstream output may contain tokenized URLs or plugin credentials. Do not relay it.
  child.stderr.on('data',classify);
  child.stdout.on('data', chunk => {
    classify(chunk);
    pending += chunk.toString('utf8');
    let end;
    while ((end=pending.indexOf('\n')) !== -1) {
      const line=pending.slice(0,end); pending=pending.slice(end+1);
      const token=launchToken(line,origin);
      if (!token || announced || stopping) continue;
      announced=true;
      publication=(async()=> {
        if (!await serverReady(host,port,origin.host) || stopping || child.exitCode !== null) throw new Error('DSH announced before HTTP readiness');
        await atomicJSON(handoff,{token,generation});
        await atomicJSON(health,{host,port,authority:origin.host,generation});
        if (stopping || child.exitCode !== null) return;
        console.log('rdsh: DSH ready; gateway handoff published');
      })().catch(() => { console.error('rdsh: could not publish DSH readiness'); process.exitCode=1; stop(); });
    }
    if (pending.length>16384) pending='';
  });
  const startup=setTimeout(()=> {
    if (!announced) { console.error('rdsh: DSH startup timed out; check runtime configuration and dependency compatibility'); process.exitCode=1; stop(); }
  },180000);
  startup.unref();
  const code=await new Promise((resolve,reject)=> {child.once('error',()=>reject(new Error('DSH process could not start')));child.once('exit',resolve);});
  clearTimeout(startup);
  clearTimeout(killTimer);
  await publication;
  if (!stopping) {
    console.error(`rdsh: DSH exited (code ${code}; categories: ${[...failures].join(',') || 'unknown'}); DSH may have saved private diagnostics under HOME/.dsh/logs`);
    process.exitCode=code===0?1:code||1;
  }
}

try { await main(); }
catch (error) {
  if (!stopping) {
    console.error(`rdsh: ${error.code === 'EACCES' ? 'runtime directory permissions do not match RDSH_UID/RDSH_GID' : error.message}`);
    process.exitCode=78;
  }
} finally {
  stop();
  await Promise.all([handoff,health].map(path=>rm(path,{force:true}).catch(()=>{})));
}
