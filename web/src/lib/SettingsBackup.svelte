<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import JobPanel from './JobPanel.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t } from '../i18n.svelte.js'

  // Encrypted copies of the settings: users (with their passwords), shares,
  // backup jobs, alerts, schedules... kept on a data volume every night,
  // downloadable, and restorable after a reinstall.
  let info = $state(null)
  let error = $state('')
  let notice = $state('')
  let dialog = $state(null)
  let job = $state(null)
  let pass1 = $state('')
  let pass2 = $state('')
  let restorePass = $state('')
  let source = $state(null) // {volume, name} or {file, name}

  async function load() {
    try {
      info = await api.get('/api/settings-backup')
    } catch (e) {
      error = e.message
    }
  }
  onMount(load)

  const when = (d) => new Date(d).toLocaleString([], { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
  const MIN = 10

  async function saveOptions() {
    error = notice = ''
    try {
      await api.post('/api/settings-backup', { nightly: info.nightly, volume: info.volume })
      notice = t('Saved.')
      load()
    } catch (e) {
      error = e.message
    }
  }

  function askPassphrase() {
    dialog = {
      title: info.passphraseSet ? t('Change the passphrase?') : t('Set the passphrase?'),
      message: t('Copies made from now on can only be opened with this passphrase. Keep it somewhere else than on the NAS (a password manager, a note at home): without it, a copy is useless.'),
      askPassword: true,
      danger: false,
      confirmLabel: t('Save passphrase'),
      onConfirm: async (password) => {
        await api.post('/api/settings-backup', { nightly: info.nightly, volume: info.volume, passphrase: pass1, password })
        pass1 = pass2 = ''
        dialog = null
        notice = t('Passphrase saved.')
        load()
      },
    }
  }

  async function copyNow() {
    error = notice = ''
    try {
      await api.post('/api/settings-backup/copy')
      notice = t('Copy written on the volume.')
    } catch (e) {
      error = e.message
    }
    load()
  }

  function askDownload() {
    dialog = {
      title: t('Download the settings?'),
      message: t('The file is encrypted with your passphrase. It holds the users and their password hashes, and the keys of your backup destinations.'),
      askPassword: true,
      danger: false,
      confirmLabel: t('Download'),
      onConfirm: async (password) => {
        const res = await fetch('/api/settings-backup/download', {
          method: 'POST',
          headers: { 'X-Requested-With': 'nasd', 'Content-Type': 'application/json' },
          body: JSON.stringify({ password }),
        })
        if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || res.statusText)
        const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') || '')?.[1] || 'nasd-settings.nasd'
        const url = URL.createObjectURL(await res.blob())
        const a = document.createElement('a')
        a.href = url
        a.download = name
        a.click()
        URL.revokeObjectURL(url)
        dialog = null
      },
    }
  }

  async function pickFile(e) {
    const f = e.currentTarget.files?.[0]
    if (!f) return
    const bytes = new Uint8Array(await f.arrayBuffer())
    let bin = ''
    for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
    source = { file: btoa(bin), name: f.name }
  }

  function askRestore() {
    dialog = {
      title: t('Restore {name}?', { name: source.name }),
      message: t('Users (with their passwords), shares, backup jobs, alerts and schedules become those of the copy. Kept as they are: the admin password, the network and firewall settings, the fan, and users created since. Nothing on the volumes is deleted.'),
      askPassword: true,
      confirmLabel: t('Restore'),
      onConfirm: async (password) => {
        job = await api.post('/api/settings-backup/restore', { ...source, passphrase: restorePass, password })
        dialog = null
        restorePass = ''
      },
    }
  }
  let busy = $derived(job?.status === 'running')
</script>

{#if error}<p class="error">{error}</p>{/if}
{#if notice}<p class="notice">{notice}</p>{/if}
{#if job}<JobPanel {job} onFinished={load} onClose={() => (job = null)} />{/if}

{#if info}
  <section class="card create">
    <h2>{t('Settings backup')}</h2>
    <p class="hint">{t('Your files are protected by the RAID and your backups. The settings are not: users, shares, backup jobs, alerts, schedules. These copies bring them back after a reinstall, users included with their passwords.')}</p>

    <h3>{t('Passphrase')} <span class="badge {info.passphraseSet ? 'good' : 'warn'}">{info.passphraseSet ? t('Set') : t('Not set')}</span></h3>
    <div class="add">
      <input type="password" bind:value={pass1} placeholder={info.passphraseSet ? t('New passphrase') : t('Passphrase')} autocomplete="new-password" aria-label={t('Passphrase')} />
      <input type="password" bind:value={pass2} placeholder={t('Repeat it')} autocomplete="new-password" aria-label={t('Repeat the passphrase')} />
      <button class="secondary" onclick={askPassphrase} disabled={pass1.length < MIN || pass1 !== pass2}>{info.passphraseSet ? t('Change') : t('Save passphrase')}</button>
    </div>
    <p class="hint">{pass1 && pass1.length < MIN ? t('At least {n} characters.', { n: MIN }) : pass2 && pass1 !== pass2 ? t("The two don't match.") : t('Every copy is encrypted with it (AES-256). Needed to restore: write it down somewhere safe.')}</p>

    <h3>{t('Nightly copy')}</h3>
    <div class="add">
      <label class="check"><input type="checkbox" bind:checked={info.nightly} disabled={!info.passphraseSet} /><span>{t('Every night at 03:30, on volume')}</span></label>
      <select bind:value={info.volume} disabled={!info.nightly} aria-label={t('Volume')}>
        <option value="">{info.volumes[0] ?? '—'}</option>
        {#each info.volumes.slice(1) as v (v)}<option value={v}>{v}</option>{/each}
      </select>
      <button class="secondary" onclick={saveOptions}>{t('Save')}</button>
    </div>
    <p class="muted small">
      {info.lastCopy ? t('Last copy: {date}', { date: when(info.lastCopy) }) : t('No copy made yet.')}
      {#if info.lastError}<span class="error"> · {info.lastError}</span>{/if}
    </p>
    <p class="hint">{t('Kept in the hidden folder .nasd-settings of the volume (readable by the system only), the last 14 days.')}</p>
    <div class="add">
      <button class="secondary" onclick={copyNow} disabled={!info.passphraseSet || !info.volumes.length}>{t('Copy now')}</button>
      <button class="secondary" onclick={askDownload} disabled={!info.passphraseSet}>{t('Download a copy')}</button>
    </div>
  </section>

  <section class="card create">
    <h2>{t('Restore')}</h2>
    <p class="hint">{t('After a reinstall: first use the data volume again (Volumes), under the same name. Its copies then show up here.')}</p>
    {#if info.copies.length}
      <table class="members">
        <thead><tr><th>{t('Copy')}</th><th>{t('Volume')}</th><th>{t('Date')}</th><th></th></tr></thead>
        <tbody>
          {#each info.copies as c (c.volume + c.name)}
            <tr class:highlight={source?.name === c.name && source?.volume === c.volume}>
              <td class="mono small">{c.name}</td><td class="mono">{c.volume}</td><td class="small">{when(c.time)} · {formatBytes(c.size)}</td>
              <td class="right"><button class="link" onclick={() => (source = { volume: c.volume, name: c.name })}>{t('Choose')}</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    {:else}
      <p class="muted small">{t('No copy found on the volumes.')}</p>
    {/if}
    <div class="add">
      <label class="file-pick">{t('Or a downloaded file:')} <input type="file" accept=".nasd" onchange={pickFile} /></label>
    </div>
    {#if source}
      <div class="add">
        <strong class="mono small">{source.name}</strong>
        <input type="password" bind:value={restorePass} placeholder={t('Its passphrase')} autocomplete="off" aria-label={t('Passphrase of the copy')} />
        <button onclick={askRestore} disabled={busy || !restorePass}>{t('Restore…')}</button>
      </div>
    {/if}
  </section>
{/if}

{#if dialog}<ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />{/if}

<style>
  h3 { display: flex; align-items: center; gap: 0.5rem; }
  .file-pick { display: inline-flex; align-items: center; gap: 0.5rem; }
</style>
