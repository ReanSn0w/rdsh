import { readFile } from 'node:fs/promises';
import { serverReady } from './protocol.mjs';
try {
  const h=JSON.parse(await readFile('/tmp/rdsh-health.json','utf8'));
  const data=await readFile(process.env.DSH_HANDOFF_FILE || '/run/rdsh/connection.json','utf8');
  if (Buffer.byteLength(data)>2048) throw new Error();
  const connection=JSON.parse(data);
  if (h.generation !== connection.generation || !/^[A-Za-z0-9_-]{43}$/.test(connection.token)) throw new Error();
  process.exitCode=await serverReady(h.host,h.port,h.authority)?0:1;
} catch { process.exitCode=1; }
