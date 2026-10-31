import { networkInterfaces } from 'node:os';
import { request } from 'node:http';
import { randomUUID } from 'node:crypto';
import { chmod, rename, rm, writeFile } from 'node:fs/promises';

export function publicOrigin(value) {
  let url;
  try { url = new URL(value); } catch { throw new Error('PUBLIC_URL must be an explicit public HTTPS origin'); }
  if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error('PUBLIC_URL must be an HTTPS origin with root path and no credentials, query or fragment');
  return url;
}

export function bindAddress(interfaces = networkInterfaces(), name) {
  const rows = name ? interfaces[name] ?? [] : Object.values(interfaces).flat();
  const addresses = [...new Set(rows.filter(row => row.family === 'IPv4' && !row.internal).map(row => row.address))];
  if (addresses.length !== 1) throw new Error('DSH needs exactly one concrete network IPv4 address; attach only its runtime network or set DSH_BIND_INTERFACE');
  return addresses[0];
}

export function launchToken(line, origin) {
  const match = /^dsh web: (https?:\/\/\S+)$/.exec(line.trim());
  if (!match) return;
  let url;
  try { url = new URL(match[1]); } catch { return; }
  const tokens = url.searchParams.getAll('token');
  if (url.origin !== origin.origin || url.pathname !== '/' || url.username || url.password || url.hash || [...url.searchParams.keys()].some(key => key !== 'token') || tokens.length !== 1 || !/^[A-Za-z0-9_-]{43}$/.test(tokens[0])) return;
  return tokens[0];
}

export async function atomicJSON(path, data) {
  const text = JSON.stringify(data);
  if (Buffer.byteLength(text) > 2048) throw new Error('Runtime state exceeds its size limit');
  const temporary = `${path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, text, { flag: 'wx', mode: 0o600 });
    await chmod(temporary, 0o600);
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force:true });
  }
}

export function serverReady(host, port, authority) {
  return new Promise(resolve => {
    const req = request({host, port, path:'/', headers:{host:authority}, method:'GET'}, response => {
      response.resume();
      resolve(response.statusCode === 401);
    });
    req.setTimeout(2000, () => req.destroy());
    req.once('error', () => resolve(false));
    req.end();
  });
}
