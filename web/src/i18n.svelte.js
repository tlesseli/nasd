// Interface languages. The English text is its own key: t('Create volume'),
// t('Delete {name}?', { name }). A text missing from a language shows in
// English. N_('...') only marks a text for translation where it is stored
// (tables, constants): it is translated where shown, with t(stored).
//
// Messages coming from the NAS itself (errors, events) stay in English.
import fr from './locales/fr.json'
import nl from './locales/nl.json'
import it from './locales/it.json'
import es from './locales/es.json'
import pt from './locales/pt.json'
import da from './locales/da.json'
import sv from './locales/sv.json'
import ro from './locales/ro.json'

export const LANGS = [
  ['en', 'English'], ['fr', 'Français'], ['nl', 'Nederlands'], ['it', 'Italiano'], ['es', 'Español'],
  ['pt', 'Português'], ['da', 'Dansk'], ['sv', 'Svenska'], ['ro', 'Română'],
]
const dicts = { fr, nl, it, es, pt, da, sv, ro }
const KEY = 'nasd.lang'

function initial() {
  try {
    const saved = localStorage.getItem(KEY)
    if (saved && LANGS.some(([id]) => id === saved)) return saved
  } catch {}
  const nav = (navigator.language || 'en').slice(0, 2).toLowerCase()
  return LANGS.some(([id]) => id === nav) ? nav : 'en'
}

export const i18n = $state({ lang: initial() })
if (typeof document !== 'undefined') document.documentElement.lang = i18n.lang

export function setLang(lang) {
  i18n.lang = lang
  document.documentElement.lang = lang
  try {
    localStorage.setItem(KEY, lang)
  } catch {}
}

// t: the text in the chosen language, with {placeholders} filled in.
export function t(text, params) {
  const s = (i18n.lang !== 'en' && dicts[i18n.lang]?.[text]) || text
  return params ? s.replace(/\{(\w+)\}/g, (m, k) => (params[k] ?? m)) : s
}

// N_: marks a stored text for translation (translated where shown with t()).
export const N_ = (text) => text

// locale: for dates and numbers (toLocaleString).
export const locale = () => i18n.lang
