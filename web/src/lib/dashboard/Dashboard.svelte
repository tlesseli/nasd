<script>
  // The Overview page in one of three styles, chosen by each viewer (kept in
  // this browser): Classic, Control panel, Neon.
  import Overview from '../Overview.svelte'
  import ControlPanel from './ControlPanel.svelte'
  import Neon from './Neon.svelte'
  import { t, N_ } from '../../i18n.svelte.js'

  let { user, onSessionLost, problems = [] } = $props()

  const KEY = 'nasd.dashboard'
  const styles = [['classic', N_('Classic')], ['panel', N_('Control panel')], ['neon', N_('Neon')]]
  let style = $state((() => {
    try {
      const s = localStorage.getItem(KEY)
      return styles.some(([id]) => id === s) ? s : 'classic'
    } catch {
      return 'classic'
    }
  })())
  function choose(id) {
    style = id
    try {
      localStorage.setItem(KEY, id)
    } catch {}
  }
</script>

<div class="style-bar" role="group" aria-label={t('Dashboard style')}>
  <span class="muted small">{t('Dashboard')}</span>
  <div class="segmented-btns">
    {#each styles as [id, label]}<button class:on={style === id} aria-pressed={style === id} onclick={() => choose(id)}>{t(label)}</button>{/each}
  </div>
</div>

{#if style === 'panel'}
  <main class="page wide"><ControlPanel {onSessionLost} /></main>
{:else if style === 'neon'}
  <main class="page wide"><Neon {user} {onSessionLost} /></main>
{:else}
  <Overview {user} {onSessionLost} {problems} />
{/if}

<style>
  .style-bar { display: flex; align-items: center; justify-content: flex-end; gap: 0.6rem; padding: 10px 24px 0; }
  .wide { max-width: none; }
</style>
