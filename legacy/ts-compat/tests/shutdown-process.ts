// Windows child.kill(SIGTERM) terminates a process without delivering a catchable signal. Tests
// reach the same registered shutdown handler through IPC there; Unix continues to use a real signal.
import type { ChildProcess } from 'node:child_process';

export function requestShutdown(child: ChildProcess) {
  if (process.platform === 'win32') child.send({ testShutdown: true });
  else child.kill('SIGTERM');
}

export const shutdownTransport = `
  if (process.platform === 'win32') process.on('message', message => {
    if (message?.testShutdown) process.emit('SIGTERM');
  });
`;
