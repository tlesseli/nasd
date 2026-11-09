<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  // Scheduled consistency check ("scrub") and filesystem check of one volume.
  let { volume, busy, onJob } = $props()

  let m = $state(null)
  let time = $state('03:00')
  let msg = $state(null) // {ok, text}
  const days = [N_('Sunday'), N_('Monday'), N_('Tuesday'), N_('Wednesday'), N_('Thursday'), N_('Friday'), N_('Saturday')]

  onMount(async () => {
    m = await api.get(`/api/volumes/${volume.name}/maintenance`).catch(() => null)
    if (m) time = `${String(m.scrub.hour).padStart(2, '0')}:${String(m.scrub.minute).padStart(2, '0')}`
  })

  async function save() {
    const [hour, minute] = time.split(':').map(Number)
    try {
      await api.post(`/api/volumes/${volume.name}/maintenance`, {
        ...m, scrub: { ...m.scrub, weekday: Number(m.scrub.weekday), hour: hour || 0, minute: minute || 0 },
      })
      msg = { ok: true, text: t('Saved.') }
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  async function check(action) {
    try {
      await api.post(`/api/volumes/${volume.name}/check`, { action })
      msg = { ok: true, text: action === 'check' ? t("Check started: progress shows above. You'll get an event when it ends.") : t("Repair started: progress shows above. You'll get an event when it ends.") }
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  async function fsCheck() {
    try {
      const r = await api.post(`/api/volumes/${volume.name}/fscheck`)
      if (r?.atBoot) msg = { ok: true, text: t('The ext4 filesystem will be checked at the next restart (it can only be checked unmounted).') }
      else onJob(r)
    } catch (e) {
      msg = { ok: false, text: e.message }
    }
  }

  let lastResult = $derived(
    volume.lastSyncAction === 'check'
      ? volume.mismatchCount === 0 ? t('Last check: no problem found.') : t('Last check found {n} inconsistent sectors.', { n: volume.mismatchCount })
      : volume.lastSyncAction === 'repair' ? t('Last repair rewrote {n} sectors.', { n: volume.mismatchCount }) : t('Never checked since the NAS started.'),
  )
</script>

{#if volume.level !== 'raid0' && m}
  <details class="maintenance">
    <summary>{t('Maintenance')} <span class="muted small">· {lastResult}</span></summary>
    <p class="muted small">{t('A check reads every block and compares it with its mirror or parity. It finds silently damaged sectors while the RAID can still fix them. It takes hours on big disks; the volume stays usable.')}</p>
    <div class="add">
      <select bind:value={m.scrub.kind} aria-label={t('Check schedule')}>
        <option value="">{t('No scheduled check')}</option>
        <option value="daily">{t('Check every day')}</option>
        <option value="weekly">{t('Check every week')}</option>
      </select>
      {#if m.scrub.kind === 'weekly'}
        <select bind:value={m.scrub.weekday} aria-label={t('Day')}>{#each days as d, i}<option value={i}>{t(d)}</option>{/each}</select>
      {/if}
      {#if m.scrub.kind}<input type="time" bind:value={time} aria-label={t('Time')} />{/if}
      <label class="check"><input type="checkbox" bind:checked={m.autoFix} /><span>{t('Repair automatically when a check finds problems')}</span></label>
      <button class="secondary" onclick={save}>{t('Save')}</button>
    </div>
    <div class="add">
      <button class="secondary" onclick={() => check('check')} disabled={busy || !!volume.sync}>{t('Check now')}</button>
      {#if volume.lastSyncAction === 'check' && volume.mismatchCount > 0}
        <button class="secondary" onclick={() => check('repair')} disabled={busy || !!volume.sync}>{t('Repair now')}</button>
      {/if}
      <button class="secondary" onclick={fsCheck} disabled={busy}>{t('Check filesystem')}</button>
      {#if m.fsckAtBoot}<span class="muted small">{t('Filesystem check planned for the next restart.')}</span>{/if}
    </div>
    {#if msg}<p class={msg.ok ? 'good-text small' : 'error small'}>{msg.text}</p>{/if}
  </details>
{/if}
