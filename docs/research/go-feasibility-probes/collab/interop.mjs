// Cross-language probe: bun interop.mjs <node_modules dir> <testdata dir>
// Applies Go-produced updates in Yjs and writes Yjs-produced fixtures for Go.
import { createRequire } from 'node:module'
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
import { join } from 'node:path'
const Y = createRequire(join(process.argv[2], 'x.js'))('yjs')
const dir = process.argv[3]
let fail = 0
for (const lib of ['reearth', 'deln0r']) {
  const f = join(dir, `go-${lib}.bin`)
  if (!existsSync(f)) { console.log(`${lib}: no fixture`); fail++; continue }
  const d = new Y.Doc()
  Y.applyUpdate(d, readFileSync(f))
  const want = readFileSync(join(dir, `go-${lib}.txt`), 'utf8')
  const got = d.getText('t').toString()
  const rp = Y.decodeRelativePosition(readFileSync(join(dir, `go-${lib}.relpos`)))
  const abs = Y.createAbsolutePositionFromRelativePosition(rp, d)
  const wantIdx = Number(readFileSync(join(dir, `go-${lib}.relidx`), 'utf8'))
  const pend = d.store.pendingStructs !== null
  const ok = got === want && abs && abs.index === wantIdx && !pend
  if (!ok) fail++
  console.log(`go(${lib}) -> yjs: text ${got === want ? 'equal' : 'DIFF'}; relpos ${abs?.index} want ${wantIdx}; pending=${pend} => ${ok ? 'PASS' : 'FAIL'}`)
}
// Yjs-produced fixture: 2 concurrent clients, emoji/combining, deletes, V1 and V2.
const a = new Y.Doc(); a.clientID = 101
const b = new Y.Doc(); b.clientID = 202
const ta = a.getText('t'), tb = b.getText('t')
ta.insert(0, 'hello 😀 world é 👩‍👩‍👧')
Y.applyUpdate(b, Y.encodeStateAsUpdate(a))
ta.insert(6, 'A中'); tb.insert(6, 'B🎉'); tb.delete(0, 2); ta.delete(ta.length - 3, 1)
Y.applyUpdate(a, Y.encodeStateAsUpdate(b, Y.encodeStateVector(a)))
Y.applyUpdate(b, Y.encodeStateAsUpdate(a, Y.encodeStateVector(b)))
if (ta.toString() !== tb.toString()) { console.log('yjs self-diverged'); fail++ }
writeFileSync(join(dir, 'js.bin'), Y.encodeStateAsUpdate(a))
writeFileSync(join(dir, 'js.v2.bin'), Y.encodeStateAsUpdateV2(a))
writeFileSync(join(dir, 'js.txt'), ta.toString())
const idx = ta.toString().indexOf('world') // JS index == UTF-16 units
writeFileSync(join(dir, 'js.relpos'), Y.encodeRelativePosition(Y.createRelativePositionFromTypeIndex(ta, idx)))
writeFileSync(join(dir, 'js.relidx'), String(idx))
// Incremental updates stream (for out-of-order apply in Go).
const c = new Y.Doc(); c.clientID = 303; const ups = []
c.on('update', u => ups.push(u))
for (let i = 0; i < 50; i++) c.getText('t').insert(i % 3 === 0 ? 0 : c.getText('t').length, i % 5 === 0 ? '🙂' : String(i % 10))
c.getText('t').delete(3, 4)
writeFileSync(join(dir, 'js-stream.json'), JSON.stringify(ups.map(u => Buffer.from(u).toString('base64'))))
writeFileSync(join(dir, 'js-stream.txt'), c.getText('t').toString())
// Differential: replay Go-generated op lists in Yjs and compare texts.
const df = join(dir, 'differential.json')
if (existsSync(df)) {
  const tally = { reearth: 0, deln0r: 0 }; let n = 0
  for (const fx of JSON.parse(readFileSync(df, 'utf8'))) {
    const rs = [new Y.Doc(), new Y.Doc()]; rs[0].clientID = 1; rs[1].clientID = 2
    const pend = [[], []]
    for (const o of fx.ops) {
      const r = rs[o.rep], t = r.getText('t'); let u
      const h = x => { u = x }; r.on('update', h)
      if (o.del) t.delete(o.idx, o.n); else t.insert(o.idx, o.str)
      r.off('update', h)
      pend[1 - o.rep].push(u)
      if (o.sync) { for (const x of pend[1 - o.rep]) Y.applyUpdate(rs[1 - o.rep], x); pend[1 - o.rep] = [] }
    }
    for (const k of [0, 1]) for (const x of pend[k]) Y.applyUpdate(rs[k], x)
    const want = rs[0].getText('t').toString(); n++
    if (want !== rs[1].getText('t').toString()) console.log(`seed ${fx.seed}: yjs self-diverged`)
    for (const lib of ['reearth', 'deln0r']) if (fx.texts[lib] === want) tally[lib]++
  }
  console.log(`differential vs yjs: ${n} seeds; identical text reearth=${tally.reearth}/${n} deln0r=${tally.deln0r}/${n}`)
  if (tally.reearth !== n) fail++
}
console.log(`yjs ${createRequire(join(process.argv[2], 'x.js'))('yjs/package.json').version} fixtures written; fail=${fail}`)
process.exit(fail ? 1 : 0)
