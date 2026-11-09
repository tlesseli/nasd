// Translation catalog tool.
//
//   node scripts/i18n.mjs            report per language: missing, obsolete, broken placeholders
//   node scripts/i18n.mjs --todo xx  print the texts language xx still lacks (JSON list)
//   node scripts/i18n.mjs --prune    remove obsolete entries from every language file
//
// Texts are collected from t('...') and N_('...') calls (first argument, a
// plain string literal) in src/**/*.svelte and src/**/*.js.
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', 'src')
const localesDir = join(root, 'locales')

function files(dir) {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    if (statSync(p).isDirectory()) return f === 'locales' ? [] : files(p)
    return /\.(svelte|js)$/.test(f) ? [p] : []
  })
}

// A string literal right after t( or N_( : '...', "..." or `...` (no ${}).
const callRe = /(?<![\w.$])(?:t|N_)\(\s*('(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"|`(?:[^`\\$]|\\.)*`)/g
function unquote(lit) {
  const q = lit[0], body = lit.slice(1, -1)
  if (q === '`') return body.replace(/\\(.)/g, '$1')
  return JSON.parse('"' + body.replace(/\\'/g, "'").replace(/(?<!\\)"/g, '\\"') + '"')
}

export function catalog() {
  const keys = new Set()
  for (const f of files(root)) {
    for (const m of readFileSync(f, 'utf8').matchAll(callRe)) keys.add(unquote(m[1]))
  }
  return [...keys].sort()
}

const placeholders = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(',')

const keys = catalog()
const langs = readdirSync(localesDir).filter((f) => f.endsWith('.json')).map((f) => f.slice(0, -5))
const load = (l) => JSON.parse(readFileSync(join(localesDir, l + '.json'), 'utf8'))

const arg = process.argv[2]
if (arg === '--todo') {
  const dict = load(process.argv[3])
  console.log(JSON.stringify(keys.filter((k) => !dict[k]), null, 1))
} else if (arg === '--prune') {
  for (const l of langs) {
    const dict = load(l)
    const kept = Object.fromEntries(Object.entries(dict).filter(([k]) => keys.includes(k)).sort(([a], [b]) => a.localeCompare(b)))
    writeFileSync(join(localesDir, l + '.json'), JSON.stringify(kept, null, 1) + '\n')
  }
  console.log('pruned')
} else {
  console.log(`${keys.length} texts in the interface`)
  let bad = 0
  for (const l of langs) {
    const dict = load(l)
    const missing = keys.filter((k) => !dict[k])
    const obsolete = Object.keys(dict).filter((k) => !keys.includes(k))
    const broken = keys.filter((k) => dict[k] && placeholders(dict[k]) !== placeholders(k))
    bad += broken.length
    console.log(`${l}: ${keys.length - missing.length}/${keys.length} translated, ${obsolete.length} obsolete, ${broken.length} broken placeholders`)
    for (const k of broken) console.log(`   placeholders differ: ${JSON.stringify(k)} -> ${JSON.stringify(dict[k])}`)
  }
  process.exitCode = bad ? 1 : 0
}
