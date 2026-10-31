import { createHash, randomUUID } from 'node:crypto';
import { spawn } from 'node:child_process';
import { copyFile, mkdir, readFile, rename, rm, symlink, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

export function runQuiet(command, args, { cwd, env, timeout = 600000, signal } = {}) {
  return new Promise((resolve, reject) => {
    // Do not forward npm/plugin diagnostics: they can contain registry credentials.
    const child = spawn(command, args, { cwd, env, detached: true, stdio: 'ignore' });
    let killed = false;
    const terminate = () => {
      killed = true;
      try { process.kill(-child.pid, 'SIGKILL'); } catch {}
    };
    const timer = setTimeout(terminate, timeout);
    signal?.addEventListener('abort', terminate, { once: true });
    if (signal?.aborted) terminate();
    child.once('error', () => { clearTimeout(timer); signal?.removeEventListener('abort', terminate); reject(new Error(`${command} could not start`)); });
    child.once('exit', code => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', terminate);
      if (code === 0 && !killed) resolve();
      else reject(new Error(killed ? `${command} interrupted or timed out` : `${command} failed (exit ${code}); check network, registry availability and available disk space`));
    });
  });
}

async function usable(directory, version, digest) {
  try {
    const marker = JSON.parse(await readFile(join(directory, '.rdsh-ready.json'), 'utf8'));
    const installed = JSON.parse(await readFile(join(directory, 'node_modules/@deepseek-ai/dsh/package.json'), 'utf8'));
    return marker.version === version && marker.digest === digest && installed.version === version;
  } catch { return false; }
}

async function activate(paths, destination) {
  const next = join(paths.state, `.current-${randomUUID()}`);
  try {
    await symlink(destination, next);
    await rename(next, join(paths.state, 'current'));
  } finally {
    await rm(next, { force:true });
  }
}

export async function ensureInstalled({ version, manifests, paths, env, signal, run = runQuiet, log = console.log }) {
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version ?? '')) throw new Error('DSH_VERSION must be an exact supported version');
  const manifest = join(manifests, version);
  let packageText, lockText;
  try {
    [packageText, lockText] = await Promise.all(['package.json', 'package-lock.json'].map(name => readFile(join(manifest, name), 'utf8')));
  } catch { throw new Error('DSH_VERSION has no bundled lock; update the wrapper image to a supported release'); }
  const spec = JSON.parse(packageText);
  const lock = JSON.parse(lockText);
  if (spec.dependencies?.['@deepseek-ai/dsh'] !== version || lock.packages?.['node_modules/@deepseek-ai/dsh']?.version !== version) throw new Error('Bundled release manifest does not match DSH_VERSION');
  const digest = createHash('sha256').update(packageText).update(lockText).digest('hex');
  const destination = join(paths.releases, `${version}-${digest.slice(0, 16)}`);
  if (await usable(destination, version, digest)) {
    let runnable=false;
    try {
      await run(process.execPath, [join(destination, 'node_modules/@deepseek-ai/dsh/lib/bin.js'), '--version'], {cwd:destination, env, signal, timeout:30000});
      runnable=true;
    } catch {
      if (signal?.aborted) throw new Error('DSH startup interrupted');
      log('rdsh: installed DSH failed its local executable check; reinstalling pinned release');
    }
    if (runnable) {
      await activate(paths,destination);
      log('rdsh: using installed DSH version');
      return destination;
    }
  }
  const staging = join(paths.releases, `.install-${randomUUID()}`);
  await mkdir(staging, { mode: 0o700 });
  try {
    for (const name of ['package.json', 'package-lock.json']) await copyFile(join(manifest, name), join(staging, name));
    log('rdsh: installing pinned DSH dependencies; this may take several minutes');
    const installEnv = { ...env, npm_config_cache: paths.npmCache, npm_config_registry: 'https://registry.npmjs.org/', npm_config_userconfig: '/dev/null', npm_config_update_notifier: 'false', npm_config_fetch_retries: '2', npm_config_fetch_timeout: '30000' };
    await run('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund'], { cwd: staging, env: installEnv, signal });
    // Reviewed helper from the exact locked package: only restores node-pty's executable bit.
    await run(process.execPath, [join(staging, 'node_modules/@deepseek-ai/dsh-subprocess-local/scripts/ensure-spawn-helper.mjs')], { cwd: staging, env: installEnv, signal, timeout: 30000 });
    await run(process.execPath, [join(staging, 'node_modules/@deepseek-ai/dsh/lib/bin.js'), '--version'], { cwd: staging, env: installEnv, signal, timeout: 30000 });
    await writeFile(join(staging, '.rdsh-ready.json'), JSON.stringify({version, digest}), { mode: 0o600 });
    if (!await usable(staging, version, digest)) throw new Error('Installed DSH version does not match its manifest');
    // Preserve a damaged prior installation for inspection; never remove user projects/settings.
    try { await rename(destination, `${destination}.incomplete-${randomUUID()}`); } catch (error) { if (error.code !== 'ENOENT') throw error; }
    await rename(staging, destination);
    await activate(paths,destination);
    log('rdsh: pinned DSH installation ready');
    return destination;
  } catch (error) {
    if (error.code === 'ENOSPC') throw new Error('DSH installation failed: no space left in HOME');
    throw error;
  } finally {
    await rm(staging, { recursive: true, force: true });
  }
}
