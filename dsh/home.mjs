import { constants } from 'node:fs';
import { access, mkdir, realpath, stat } from 'node:fs/promises';
import { isAbsolute, join } from 'node:path';

export async function prepareHome(home) {
  if (!home || !isAbsolute(home) || home === '/') throw new Error('HOME must be a dedicated absolute directory');
  let info;
  try {
    info = await stat(home);
    await access(home, constants.R_OK | constants.W_OK | constants.X_OK);
  } catch {
    throw new Error('HOME is missing or not writable: prepare the bind mount with the configured RDSH_UID/RDSH_GID');
  }
  if (!info.isDirectory() || (info.mode & 0o002) !== 0) throw new Error('HOME must be a directory without world-write permission');
  if (process.getuid && info.uid !== process.getuid()) throw new Error('HOME owner does not match RDSH_UID; fix ownership of the dedicated directory on the host');
  const root = await realpath(home);
  const paths = {
    state: join(root, '.rdsh'), releases: join(root, '.rdsh/releases'), cache: join(root, '.cache'),
    npmCache: join(root, '.cache/npm'), nativeCache: join(root, '.cache/native'),
    dshHome: join(root, '.dsh'), workspace: join(root, 'workspaces'),
  };
  for (const path of Object.values(paths)) {
    await mkdir(path, { recursive: true, mode: 0o700 });
    const actual = await realpath(path);
    if (!actual.startsWith(`${root}/`)) throw new Error('Managed HOME directory resolves outside the dedicated home');
    await access(path, constants.R_OK | constants.W_OK | constants.X_OK);
  }
  return paths;
}
