// Test-only client. Credentials remain inside the temporary container home.
import assert from 'node:assert/strict';
import { readFile, writeFile, rm } from 'node:fs/promises';
import { request } from 'node:http';
const state='/home/dsh/.rdsh/lifecycle-fixture.json';
const marker='/home/dsh/workspaces/lifecycle-fixture.txt';
const health=JSON.parse(await readFile('/tmp/rdsh-health.json','utf8'));
const current=JSON.parse(await readFile('/run/rdsh/connection.json','utf8'));
function get(path,cookie) {
  return new Promise((resolve,reject)=>{
    const req=request({host:health.host,port:health.port,path,headers:{host:health.authority,...(cookie?{cookie}:{})}},res=>{
      res.resume(); res.on('end',()=>resolve({status:res.statusCode,headers:res.headers}));
    });
    req.on('error',()=>reject(new Error('HTTP request failed')));
    req.setTimeout(5000,()=>req.destroy());req.end();
  });
}
const mode=process.argv[2];
if(mode==='record') {
  assert.equal((await get('/')).status,401);
  const exchange=await get(`/?token=${encodeURIComponent(current.token)}`);
  assert.equal(exchange.status,303);
  const cookie=exchange.headers['set-cookie']?.find(x=>x.startsWith('dsh-auth-'))?.split(';')[0];
  assert.ok(cookie,'browser cookie required');
  assert.equal((await get('/',cookie)).status,200);
  await writeFile(state,JSON.stringify({...current,cookie}),{mode:0o600});
  await writeFile(marker,'preserved user workspace');
  console.log('PASS: actual wrapper token exchange and authenticated root');
} else if(mode==='compare') {
  const previous=JSON.parse(await readFile(state,'utf8'));
  assert.ok(previous.token!==current.token,'launch token must rotate');
  assert.ok(previous.generation!==current.generation,'generation must rotate');
  assert.equal((await get(`/?token=${encodeURIComponent(previous.token)}`)).status,401);
  assert.equal((await get('/',previous.cookie)).status,200);
  assert.equal(await readFile(marker,'utf8'),'preserved user workspace');
  assert.equal((await get(`/?token=${encodeURIComponent(current.token)}`)).status,303);
  console.log('PASS: token rotation, persisted browser session and workspace');
} else if(mode==='logs') {
  const previous=JSON.parse(await readFile(state,'utf8'));
  let logs=''; for await(const chunk of process.stdin) logs+=chunk;
  assert.ok(!logs.includes(current.token)&&!logs.includes(previous.token)&&!logs.includes(previous.cookie),'Docker logs must not contain credentials');
  console.log('PASS: current and previous credentials absent from Docker logs');
} else if(mode==='clean') {
  await Promise.all([state,marker].map(path=>rm(path,{force:true})));
} else throw new Error('Unknown test mode');
