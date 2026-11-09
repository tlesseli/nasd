<script>
  // Interface language, per browser (see i18n.svelte.js).
  import { i18n, LANGS, setLang, t } from '../i18n.svelte.js'
</script>

<label class="lang">
  <span class="sr-only">{t('Language')}</span>
  <select value={i18n.lang} onchange={(e) => setLang(e.currentTarget.value)} aria-label={t('Language')}>
    {#each LANGS as [id, name]}<option value={id}>{name}</option>{/each}
  </select>
</label>

<style>
  .lang { display: block; }
  .lang select { width: 100%; font-size: 0.82rem; padding: 0.3rem 0.4rem; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
</style>
