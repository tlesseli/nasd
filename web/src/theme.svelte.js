// Look of the interface, per browser: a theme for the whole admin UI
// (Classic, Control panel, Neon) and, for Classic, light/dark/automatic.
// Applied as data-theme / data-mode on <html> (see themes.css).
import { N_ } from './i18n.svelte.js'

export const THEMES = [['classic', N_('Classic')], ['panel', N_('Control panel')], ['neon', N_('Neon')]]
export const MODES = [['auto', N_('Automatic')], ['light', N_('Light')], ['dark', N_('Dark')]]

function read(key, allowed, fallback) {
  try {
    const v = localStorage.getItem(key)
    if (allowed.some(([id]) => id === v)) return v
  } catch {}
  return fallback
}

// 'nasd.dashboard' was the dashboard style before themes covered the whole UI.
export const look = $state({
  theme: read('nasd.theme', THEMES, read('nasd.dashboard', [['panel'], ['neon'], ['classic']], 'classic')),
  mode: read('nasd.mode', MODES, 'auto'),
})

function apply() {
  if (typeof document === 'undefined') return
  document.documentElement.dataset.theme = look.theme
  document.documentElement.dataset.mode = look.mode
}
apply()

export function setTheme(theme) {
  look.theme = theme
  apply()
  try {
    localStorage.setItem('nasd.theme', theme)
  } catch {}
}

export function setMode(mode) {
  look.mode = mode
  apply()
  try {
    localStorage.setItem('nasd.mode', mode)
  } catch {}
}
