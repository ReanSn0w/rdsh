import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { publicOrigin, bindAddress, launchToken, atomicJSON } from '../../dsh/protocol.mjs';
const origin=publicOrigin('https://dsh.example/');
const token='x'.repeat(43);
test('public URL is an explicit HTTPS origin',()=>{
  for(const bad of ['http://dsh.example/','https://a:b@dsh.example/','https://dsh.example/ui/','https://dsh.example/?token=x']) assert.throws(()=>publicOrigin(bad));
  assert.equal(origin.host,'dsh.example');
});
test('network selection rejects ambiguous and missing addresses',()=>{
  const one={eth0:[{family:'IPv4',internal:false,address:'172.20.0.2'}]};
  assert.equal(bindAddress(one),'172.20.0.2');
  assert.throws(()=>bindAddress({}));
  const two={...one,eth1:[{family:'IPv4',internal:false,address:'172.21.0.2'}]};
  assert.throws(()=>bindAddress(two));
  assert.equal(bindAddress(two,'eth0'),'172.20.0.2');
});
test('only a canonical exact startup line can provide the token',()=>{
  assert.equal(launchToken(`dsh web: https://dsh.example/?token=${token}`,origin),token);
  for(const line of [`message dsh web: https://dsh.example/?token=${token}`,`dsh web: https://evil.example/?token=${token}`,`dsh web: https://dsh.example/?token=${token}&token=${token}`,`dsh web: https://dsh.example/?token=short`,`dsh web: https://dsh.example/?token=${token}&x=1`]) assert.equal(launchToken(line,origin),undefined);
});
test('handoff writes atomically with private permissions and bounded size',async t=>{
  const dir=await mkdtemp(join(tmpdir(),'rdsh-handoff-'));
  t.after(()=>rm(dir,{recursive:true,force:true}));
  const file=join(dir,'connection.json');
  await atomicJSON(file,{token,generation:'first'});
  await atomicJSON(file,{token,generation:'second'});
  assert.equal(JSON.parse(await readFile(file,'utf8')).generation,'second');
  assert.equal((await stat(file)).mode&0o777,0o600);
  await assert.rejects(atomicJSON(file,{token:'x'.repeat(3000)}),/size/);
  assert.equal(JSON.parse(await readFile(file,'utf8')).generation,'second');
});
