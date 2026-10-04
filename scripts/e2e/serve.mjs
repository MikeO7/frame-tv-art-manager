import { writeFile } from 'node:fs/promises';
import { workspace, launch, stop, dispose } from './runtime.mjs';

const paths = await workspace();
await writeFile('.e2e/runtime.json', JSON.stringify(paths));
const processUnderTest = launch(paths, { HEALTH_PORT: '39060' });
processUnderTest.child.stdout.pipe(process.stdout);
processUnderTest.child.stderr.pipe(process.stderr);
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { void stop(processUnderTest); });
const [code] = await processUnderTest.exited;
await dispose(paths);
process.exitCode = code ?? 1;
