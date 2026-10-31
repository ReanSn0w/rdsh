import { prepareHome } from './home.mjs';
try {
  await prepareHome(process.env.HOME);
} catch (error) {
  console.error(`rdsh: ${error.message}`);
  process.exitCode = 78;
}
