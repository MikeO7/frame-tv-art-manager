import type { E2EConfig } from 'e2e';
import { web } from '@e2e-dev/web';
import { token } from './scripts/e2e/runtime.mjs';

export default {
  tests: 'tests/e2e/**/*.e2e.ts',
  targets: [{
    name: 'chromium',
    engine: web({ basicAuth: { username: 'frame', password: token } }),
    app: {
      url: 'http://127.0.0.1:39060',
      readyUrl: 'http://127.0.0.1:39060/live',
      environment: 'test',
      command: {
        executable: process.execPath,
        args: ['scripts/e2e/serve.mjs'],
        log: '.e2e/logs/app.log',
        shutdownTimeout: 10000,
      },
    },
  }],
  workers: 1,
  retries: 0,
  trace: 'retain-on-failure',
  cache: 'off',
  reporters: ['list', 'markdown', 'junit'],
} satisfies E2EConfig;
