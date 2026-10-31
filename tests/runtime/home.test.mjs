import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, writeFile, rm, chmod, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { prepareHome } from '../../dsh/home.mjs';

test('home preparation preserves existing user files across repeated runs', async t => {
  const home = await mkdtemp(join(tmpdir(), 'rdsh-home-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  const first = await prepareHome(home);
  await writeFile(join(first.workspace, 'user.txt'), 'keep me');
  const second = await prepareHome(home);
  assert.deepEqual(first, second);
  assert.equal(await readFile(join(second.workspace, 'user.txt'), 'utf8'), 'keep me');
});

test('unsafe and missing homes fail without creating them', async () => {
  await assert.rejects(prepareHome('/'), /dedicated/);
  await assert.rejects(prepareHome('relative'), /absolute/);
  await assert.rejects(prepareHome('/nonexistent-rdsh-home'), /bind mount/);
});

test('world-writable home is rejected without changing its permissions', async t => {
  const home = await mkdtemp(join(tmpdir(), 'rdsh-permissions-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  await chmod(home, 0o777);
  await assert.rejects(prepareHome(home), /world-write/);
});

test('managed directories cannot redirect writes outside HOME', async t => {
  const home = await mkdtemp(join(tmpdir(), 'rdsh-symlink-'));
  const external = await mkdtemp(join(tmpdir(), 'rdsh-external-'));
  t.after(() => Promise.all([home, external].map(path => rm(path, { recursive: true, force: true }))));
  await symlink(external, join(home, '.rdsh'));
  await assert.rejects(prepareHome(home), /outside/);
});

test('unwritable home reports a bind-mount permission problem',async t=>{
  const home=await mkdtemp(join(tmpdir(),'rdsh-readonly-'));
  t.after(async()=>{await chmod(home,0o700);await rm(home,{recursive:true,force:true});});
  await chmod(home,0o500);
  if(process.getuid?.()===0) return t.skip('root bypasses ordinary filesystem permissions');
  await assert.rejects(prepareHome(home),/not writable/);
});
