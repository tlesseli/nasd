<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  // When every disk runs its SMART extended test ("surface test").
  let { longMinutes = 0 } = $props()

  let sc = $state(null)
  let time = $state('02:00')
  let msg = $state(null) // {ok, text}
  const days = [N_('Sunday'), N_('Monday'), N_('Tuesday'), N_('Wednesday'), N_('Thursday'), N_('Friday'), N_('Saturday')]

  onMount(async () => {
    sc = (await api.get('/api/disks/tests').catch(() => null))?.schedule ?? null
    if (sc) {
      if (sc.kind) time = `${String(sc.hour).padStart(2, '0')}:${String(sc.minute).padStart(2, '0')}`
      sc.day ||= 1
    }
  })

  async function save() {
    const [hour, minute] = time.split(':').map(Number)
    try {
      await api.post('/api/disks/tests', { schedule: { ...sc, weekday: Number(sc.weekday), day: Number(sc.day), hour: hour || 0, minute: minute || 0 } })
      msg = { ok: true, text: t('Saved.') }
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }
</script>

{#if sc}
  <div class="disk-tests">
    <div class="add">
      <strong>{t('Surface test')}</strong>
      <select bind:value={sc.kind} aria-label={t('Surface test schedule')}>
        <option value="">{t('Never')}</option>
        <option value="monthly">{t('Every month')}</option>
        <option value="weekly">{t('Every week')}</option>
      </select>
      {#if sc.kind === 'monthly'}
        <label class="inline">{t('on day')} <input type="number" min="1" max="28" bind:value={sc.day} aria-label={t('Day of the month')} /></label>
      {/if}
      {#if sc.kind === 'weekly'}
        <select bind:value={sc.weekday} aria-label={t('Day')}>{#each days as d, i}<option value={i}>{t(d)}</option>{/each}</select>
      {/if}
      {#if sc.kind}<input type="time" bind:value={time} aria-label={t('Time')} />{/if}
      <button class="secondary" onclick={save}>{t('Save')}</button>
      {#if msg}<span class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</span>{/if}
    </div>
    <p class="hint">
      {t('Each disk reads its whole surface by itself (SMART extended test) and reports sectors it can no longer read, before a rebuild needs them. The NAS stays usable, a little slower.')}
      {#if longMinutes}{t('It takes about {time} on these disks.', { time: longMinutes >= 120 ? t('{n} hours', { n: Math.round(longMinutes / 60) }) : t('{n} minutes', { n: longMinutes }) })}{/if}
      {t('Pick another day than the volume check.')}
    </p>
  </div>
{/if}

<style>
  .disk-tests { padding: 0.75rem 1.25rem 1rem; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 0.4rem; }
  .disk-tests input[type='number'] { width: 4.5rem; }
  .inline { display: inline-flex; align-items: center; gap: 0.4rem; }
</style>
