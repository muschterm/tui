// Isolated evidence, not an application dependency or selected editor algorithm.
// Install the pinned dependencies in a temporary directory; pass that directory.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {Writable, PassThrough} from 'node:stream';
import {pathToFileURL} from 'node:url';
import {resolve} from 'node:path';
import {mkdtempSync, writeFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {spawnSync} from 'node:child_process';

const requireProbe = createRequire(resolve(process.argv[2], 'package.json'));
const Y = await import(pathToFileURL(requireProbe.resolve('yjs')).href);
const React = await import(pathToFileURL(requireProbe.resolve('react')).href);
const {render, Text} = await import(pathToFileURL(requireProbe.resolve('ink')).href);

const a = new Y.Doc();
const b = new Y.Doc();
const ta = a.getText('text');
const tb = b.getText('text');
ta.insert(0, 'base');
Y.applyUpdate(b, Y.encodeStateAsUpdate(a));
const ownA = new Y.UndoManager(ta, {trackedOrigins: new Set(['a']), captureTimeout: 0});
a.transact(() => ta.insert(0, 'A'), 'a');
b.transact(() => tb.insert(0, 'B'), 'b');
const ua = Y.encodeStateAsUpdate(a);
const ub = Y.encodeStateAsUpdate(b);
Y.applyUpdate(a, ub);
Y.applyUpdate(b, ua);
assert.equal(ta.toString(), tb.toString());
Y.applyUpdate(a, ub); // Retry cannot duplicate content.
assert.equal(ta.toString(), tb.toString());
ownA.undo();
Y.applyUpdate(b, Y.encodeStateAsUpdate(a));
assert.equal(ta.toString(), 'Bbase');
assert.equal(tb.toString(), 'Bbase');
a.transact(() => ta.insert(ta.length, '🙂'), 'a');
b.transact(() => tb.delete(0, 1), 'b');
const ua2 = Y.encodeStateAsUpdate(a);
const ub2 = Y.encodeStateAsUpdate(b);
Y.applyUpdate(a, ub2);
Y.applyUpdate(b, ua2);
assert.equal(ta.toString(), 'base🙂');
assert.equal(ta.toString(), tb.toString());
console.log('PASS Yjs: concurrent insert/delete convergence, duplicate delivery, origin-scoped undo, emoji text');

// This checks a merge primitive, not autosave races or an accepted merge policy.
const mergeDir = mkdtempSync(resolve(tmpdir(), 'tui-merge-probe-'));
try {
  const base = 'one\ntwo\nthree\nfour\nfive\n';
  const local = 'ONE\ntwo\nthree\nfour\nfive\n';
  const external = 'one\ntwo\nthree\nfour\nFIVE\n';
  for (const [name, content] of Object.entries({base, local, external})) {
    writeFileSync(resolve(mergeDir, name), content);
  }
  const merge = () => spawnSync('git', ['merge-file', '-p', 'local', 'base', 'external'], {cwd: mergeDir, encoding: 'utf8'});
  const clean = merge();
  assert.equal(clean.status, 0);
  assert.equal(clean.stdout, 'ONE\ntwo\nthree\nfour\nFIVE\n');
  writeFileSync(resolve(mergeDir, 'external'), 'different\ntwo\nthree\nfour\nfive\n');
  const conflict = merge();
  assert.equal(conflict.status, 1);
  assert.match(conflict.stdout, /<<<<<<< local/);
  console.log('PASS merge primitive: disjoint external changes merge; overlapping edits report conflict');
} finally {
  rmSync(mergeDir, {recursive: true, force: true});
}

let frame = '';
const stdout = new Writable({write(chunk, encoding, done) { frame += chunk.toString(); done(); }});
stdout.columns = 40;
stdout.rows = 8;
stdout.isTTY = false;
const instance = render(React.createElement(Text, {}, 'ink-probe'), {
  stdout, stderr: stdout, stdin: new PassThrough(), debug: true,
  exitOnCtrlC: false, patchConsole: false,
});
await new Promise(resolve => setTimeout(resolve, 50));
instance.unmount();
await instance.waitUntilExit();
assert.match(frame, /ink-probe/);
console.log('PASS Ink: React render and unmount under Bun (non-TTY stream only)');

let bytes = '';
const decoder = new TextDecoder();
const child = Bun.spawn(['/bin/sh', '-c', 'stty size; read value; stty size; printf "received:%s" "$value"'], {
  terminal: {cols: 42, rows: 9, data(terminal, chunk) {
    bytes += decoder.decode(chunk, {stream: true});
    if (bytes.includes('9 42') && !bytes.includes('resize-sent')) {
      bytes += '[resize-sent]';
      terminal.resize(51, 12);
      terminal.write('hello\n');
    }
  }},
});
const timeout = setTimeout(() => child.kill(), 5000);
const status = await child.exited;
clearTimeout(timeout);
child.terminal.close();
assert.equal(status, 0);
assert.match(bytes, /12 51/);
assert.match(bytes, /received:hello/);
assert.equal(child.terminal.closed, true);
console.log('PASS Bun PTY: initial size, resize, input, output, process exit, explicit close');
