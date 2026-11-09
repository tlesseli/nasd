<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import { t, N_, i18n, LANGS, setLang } from '../i18n.svelte.js'
  import { look, THEMES, MODES, setTheme, setMode } from '../theme.svelte.js'

  // How the NAS identifies itself, and its clock. Changes are collected and
  // saved together (the bar at the bottom), like in the mockup.
  let { hostname } = $props()

  let initial = $state(null) // values as loaded
  let form = $state(null)
  let zones = $state([])
  let settings = $state(null)
  let timeInfo = $state(null)
  let busy = $state(false)
  let msg = $state(null)

  async function load() {
    const [tm, st] = await Promise.all([api.get('/api/time'), api.get('/api/settings')])
    zones = tm.timezones
    timeInfo = tm.info
    settings = st
    initial = {
      name: hostname, workgroup: st.workgroup || 'WORKGROUP', timezone: tm.info.timezone,
      ntp: tm.info.ntp, servers: tm.info.ntpServers.join(', '), manual: '',
    }
    form = { ...initial }
  }
  onMount(load)

  const labels = { name: N_('Device name'), workgroup: N_('Workgroup'), timezone: N_('Time zone'), ntp: N_('Automatic time'), servers: N_('NTP servers'), manual: N_('Manual time') }
  const changed = $derived(form ? Object.keys(labels).filter((k) => form[k] !== initial[k]) : [])

  async function waitForRestart() {
    msg = { ok: true, text: t('Saved. The NAS service restarts to apply the new name or time zone; reloading…') }
    await new Promise((r) => setTimeout(r, 4000))
    for (let i = 0; i < 30; i++) {
      try {
        await api.get('/api/setup')
        location.reload()
        return
      } catch {
        await new Promise((r) => setTimeout(r, 1000))
      }
    }
  }

  async function save() {
    busy = true
    msg = null
    try {
      if (form.workgroup !== initial.workgroup) await api.post('/api/settings', { ...settings, workgroup: form.workgroup })
      if (['timezone', 'ntp', 'servers', 'manual'].some((k) => changed.includes(k))) {
        await api.post('/api/time', { timezone: form.timezone, ntp: form.ntp,
          ntpServers: form.servers.split(/[\s,]+/).filter(Boolean), manual: form.ntp ? '' : form.manual })
      }
      if (form.name !== initial.name) await api.post('/api/hostname', { name: form.name })
      if (form.name !== initial.name || form.timezone !== initial.timezone) return waitForRestart()
      msg = { ok: true, text: t('Saved.') }
      await load()
    } catch (e) {
      msg = { ok: false, text: e.message }
    } finally {
      busy = false
    }
  }
</script>

<!-- Look and language: kept in this browser, applied at once (no Save). -->
<section class="card appearance">
  <div class="card-head"><div><h2>{t('Appearance')}</h2><div class="muted small">{t('For this browser. Applied immediately.')}</div></div></div>
  <div class="themes" role="radiogroup" aria-label={t('Theme')}>
    {#each THEMES as [id, label]}
      <button type="button" class="theme-choice {id}" class:on={look.theme === id} role="radio" aria-checked={look.theme === id} onclick={() => setTheme(id)}>
        <span class="swatch" aria-hidden="true"><i></i><i></i><i></i></span>
        <span>{t(label)}</span>
      </button>
    {/each}
  </div>
  <div class="two-cols">
    {#if look.theme === 'classic'}
      <label>{t('Color mode')}
        <select value={look.mode} onchange={(e) => setMode(e.currentTarget.value)}>
          {#each MODES as [id, label]}<option value={id}>{t(label)}</option>{/each}
        </select>
      </label>
    {/if}
    <label>{t('Language')}
      <select value={i18n.lang} onchange={(e) => setLang(e.currentTarget.value)}>
        {#each LANGS as [id, name]}<option value={id}>{name}</option>{/each}
      </select>
    </label>
  </div>
</section>

{#if form}
  <section class="card">
    <div class="card-head"><div><h2>{t('General')}</h2><div class="muted small">{t('How this device identifies itself.')}</div></div></div>
    <div class="form-row">
      <div class="what">{t('Device name')}<small>{t('Shown on the network, in Finder and Explorer. Max 15 characters.')}</small></div>
      <input bind:value={form.name} maxlength="15" spellcheck="false" class="mono" aria-label={t('Device name')} />
    </div>
    <div class="form-row">
      <div class="what">{t('Workgroup')}<small>{t('SMB workgroup (Windows network neighbourhood).')}</small></div>
      <input bind:value={form.workgroup} maxlength="15" spellcheck="false" class="mono" aria-label={t('Workgroup')} />
    </div>
    <div class="form-row">
      <div class="what">{t('Time zone')}<small>{timeInfo.synced ? t('Used for schedules and logs. Now: {now} · synchronized', { now: new Date(timeInfo.now).toLocaleString() }) : t('Used for schedules and logs. Now: {now}', { now: new Date(timeInfo.now).toLocaleString() })}</small></div>
      <select bind:value={form.timezone} aria-label={t('Time zone')}>{#each zones as z}<option>{z}</option>{/each}</select>
    </div>
    <div class="form-row">
      <div class="what">{t('Automatic time')}<small>{t('From internet time servers (NTP).')}</small></div>
      <label class="check"><input type="checkbox" bind:checked={form.ntp} /> <span>{t('Set the time automatically')}</span></label>
    </div>
    {#if form.ntp}
      <div class="form-row">
        <div class="what">{t('NTP servers')}<small>{t("Comma-separated; empty = Debian's default pool.")}</small></div>
        <input bind:value={form.servers} placeholder="pool.ntp.org, time.cloudflare.com" spellcheck="false" class="mono" aria-label={t('NTP servers')} />
      </div>
    {:else}
      <div class="form-row">
        <div class="what">{t('Set the time to')}<small>{t('Format 2026-11-08 20:15:00.')}</small></div>
        <input bind:value={form.manual} placeholder="2026-11-08 20:15:00" class="mono" aria-label={t('Manual time')} />
      </div>
    {/if}
  </section>

  {#if msg}<p class={msg.ok ? 'notice' : 'error'}>{msg.text}</p>{/if}
  {#if changed.length}
    <div class="savebar">
      <span class="small">{changed.length > 1 ? t('{n} unsaved changes · {fields}', { n: changed.length, fields: changed.map((k) => t(labels[k])).join(', ') }) : t('{n} unsaved change · {fields}', { n: changed.length, fields: changed.map((k) => t(labels[k])).join(', ') })}</span>
      <span class="spacer"></span>
      <button class="link" onclick={() => (form = { ...initial })} disabled={busy}>{t('Discard')}</button>
      <button class="save" onclick={save} disabled={busy}>{busy ? t('Saving…') : t('Save changes')}</button>
    </div>
  {/if}
{/if}

<style>
  .appearance { margin-bottom: 14px; }
  .themes { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; margin-bottom: 1rem; }
  .theme-choice { display: flex; flex-direction: column; align-items: flex-start; gap: 0.5rem; background: var(--surface-2); color: var(--text);
    border: 2px solid var(--border); text-transform: none; letter-spacing: 0; font-weight: 600; box-shadow: none; padding: 0.7rem; }
  .theme-choice.on { border-color: var(--accent); }
  .swatch { display: flex; width: 100%; height: 34px; border-radius: 4px; overflow: hidden; }
  .swatch i { flex: 1; }
  .classic .swatch i:nth-child(1) { background: #0b0c0f; } .classic .swatch i:nth-child(2) { background: #f5f6f8; } .classic .swatch i:nth-child(3) { background: #8b8ff8; }
  .panel .swatch i:nth-child(1) { background: #ddd8c4; } .panel .swatch i:nth-child(2) { background: #23262b; } .panel .swatch i:nth-child(3) { background: #f6b52e; }
  .neon .swatch i:nth-child(1) { background: #060a18; } .neon .swatch i:nth-child(2) { background: #36d1ff; } .neon .swatch i:nth-child(3) { background: #b56cff; }

  .save { background: var(--accent); color: #fff; }
</style>
