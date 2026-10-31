import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, readlink, rm, cp } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { prepareHome } from '../../dsh/home.mjs';
import { ensureInstalled, runQuiet } from '../../dsh/install.mjs';

async function fixture(t) {
  const home = await mkdtemp(join(tmpdir(), 'rdsh-install-'));
  t.after(() => rm(home, {recursive:true, force:true}));
  const paths = await prepareHome(home), manifests = join(home, 'manifests');
  const version = '1.2.3';
  await mkdir(join(manifests, version), {recursive:true});
  await writeFile(join(manifests, version, 'package.json'), JSON.stringify({dependencies:{'@deepseek-ai/dsh':version}}));
  await writeFile(join(manifests, version, 'package-lock.json'), JSON.stringify({packages:{'node_modules/@deepseek-ai/dsh':{version}}}));
  return {paths, manifests, version, env:process.env, log:()=>{}};
}
const successfulInstall = async (command, args, {cwd}) => {
  if (command !== 'npm') return;
  assert.ok(args.includes('--ignore-scripts'));
  await mkdir(join(cwd, 'node_modules/@deepseek-ai/dsh'), {recursive:true});
  await writeFile(join(cwd, 'node_modules/@deepseek-ai/dsh/package.json'), '{"version":"1.2.3"}');
};
test('successful installation is reused without invoking npm', async t => {
  const f = await fixture(t);
  const first = await ensureInstalled({...f,run:successfulInstall});
  const second = await ensureInstalled({...f,run:command=>assert.notEqual(command,'npm','must not reinstall')});
  assert.equal(first,second);
  assert.equal(await readlink(join(f.paths.state,'current')),first);
});
test('broken executable is repaired even when its manifest and marker remain',async t=>{
  const f=await fixture(t);
  await ensureInstalled({...f,run:successfulInstall});
  let checked=false,installs=0;
  await ensureInstalled({...f,run:async(command,args,options)=>{
    if(!checked){checked=true;throw new Error('missing bin.js');}
    if(command==='npm')installs++;
    return successfulInstall(command,args,options);
  }});
  assert.equal(installs,1);
});
test('failed installation preserves user files and a retry can succeed', async t => {
  const f = await fixture(t);
  await writeFile(join(f.paths.workspace,'keep.txt'),'project');
  await assert.rejects(ensureInstalled({...f,run:()=>{throw new Error('offline');}}),/offline/);
  assert.equal(await readFile(join(f.paths.workspace,'keep.txt'),'utf8'),'project');
  await ensureInstalled({...f,run:successfulInstall});
});
test('unsupported and floating versions fail before running npm', async t => {
  const f=await fixture(t),run=()=>assert.fail('unexpected command');
  await assert.rejects(ensureInstalled({...f,version:'latest',run}),/exact/);
  await assert.rejects(ensureInstalled({...f,version:'9.9.9',run}),/bundled lock/);
});
test('installer reports disk exhaustion without destroying projects', async t => {
  const f=await fixture(t);
  await assert.rejects(ensureInstalled({...f,run:()=>{throw Object.assign(new Error('disk'),{code:'ENOSPC'});}}),/no space/);
});
test('commands time out and do not expose child diagnostics', async () => {
  await assert.rejects(runQuiet(process.execPath,['-e',"console.error('private-diagnostic');setInterval(()=>{},1000)"],{timeout:50}),/timed out/);
});

test('installation cancellation terminates the running command',async()=>{
  const controller=new AbortController();
  const promise=runQuiet(process.execPath,['-e','setInterval(()=>{},1000)'],{signal:controller.signal,timeout:30000});
  controller.abort();
  await assert.rejects(promise,/interrupted/);
});

test('explicit locked version update preserves user data and switches active installation',async t=>{
  const f=await fixture(t);
  const first=await ensureInstalled({...f,run:successfulInstall});
  await writeFile(join(f.paths.dshHome,'test-history.json'),'{"messages":["keep"]}');
  const next='1.2.4';
  await cp(join(f.manifests,f.version),join(f.manifests,next),{recursive:true});
  for(const name of ['package.json','package-lock.json']) {
    const path=join(f.manifests,next,name);
    await writeFile(path,(await readFile(path,'utf8')).replaceAll(f.version,next));
  }
  const second=await ensureInstalled({...f,version:next,run:async(command,args,options)=>{
    await successfulInstall(command,args,options);
    if(command==='npm')await writeFile(join(options.cwd,'node_modules/@deepseek-ai/dsh/package.json'),JSON.stringify({version:next}));
  }});
  assert.notEqual(first,second);
  assert.equal(await readlink(join(f.paths.state,'current')),second);
  assert.equal(await readFile(join(f.paths.dshHome,'test-history.json'),'utf8'),'{"messages":["keep"]}');
});
