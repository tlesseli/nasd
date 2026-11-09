<script>
  import { onMount } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import UserEdit from './UserEdit.svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import JobPanel from './JobPanel.svelte'
  import { t, N_ } from '../i18n.svelte.js'

  let { onSessionLost } = $props()
  let security = $state(null)
  let homes = $state(null)

  let users = $state([])
  let quotas = $state([]) // per mounted volume: {volume, filesystem, enabled, usage[]}
  let loaded = $state(false)
  let error = $state('')
  let notice = $state('')
  let dialog = $state(null)
  let job = $state(null)

  // New user form
  let adding = $state(false)
  let newName = $state('')
  let newPw = $state('')
  let newPw2 = $state('')
  let newEmail = $state('')
  let editing = $state(null) // user being edited
  let formError = $state('')

  // Password dialog (a NAS user or '__admin__')
  let pwFor = $state(null)
  let current = $state('')
  let pw = $state('')
  let pw2 = $state('')
  let pwError = $state('')

  // Quota dialog
  let quotaFor = $state(null) // {name, value, unit}
  let quotaError = $state('')

  async function refresh() {
    try {
      ;[users, quotas, security, homes] = await Promise.all([api.get('/api/users'), api.get('/api/quotas'), api.get('/api/security'), api.get('/api/homes')])
      loaded = true
    } catch (e) {
      if (e.status === 401) onSessionLost()
      else error = e.message
    }
  }
  onMount(refresh)

  async function createUser(event) {
    event.preventDefault()
    formError = ''
    if (newPw !== newPw2) return (formError = t('The passwords do not match.'))
    try {
      await api.post('/api/users', { name: newName, password: newPw, email: newEmail })
      notice = t('User {name} created. Give them access to shares on the Shares page.', { name: newName })
      adding = false
      newName = newPw = newPw2 = newEmail = ''
      refresh()
    } catch (e) {
      formError = e.message
    }
  }

  function openPassword(name) {
    pwFor = name
    current = pw = pw2 = pwError = ''
  }

  async function savePassword(event) {
    event.preventDefault()
    pwError = ''
    if (pw !== pw2) return (pwError = t('The passwords do not match.'))
    try {
      if (pwFor === '__admin__') await api.post('/api/me/password', { current, new: pw })
      else await api.post(`/api/users/${pwFor}/password`, { password: pw })
      notice = pwFor === '__admin__' ? t('Admin password changed. Other browsers were signed out.') : t('Password of {name} changed.', { name: pwFor })
      pwFor = null
    } catch (e) {
      pwError = e.message
    }
  }

  function openQuota(u) {
    const gb = u.quotaMB >= 1024 && u.quotaMB % 1024 === 0
    quotaFor = { name: u.name, value: u.quotaMB ? (gb ? u.quotaMB / 1024 : u.quotaMB) : '', unit: gb || !u.quotaMB ? 'GB' : 'MB' }
    quotaError = ''
  }

  async function saveQuota(event) {
    event.preventDefault()
    const mb = Math.round((Number(quotaFor.value) || 0) * (quotaFor.unit === 'GB' ? 1024 : 1))
    try {
      await api.post(`/api/users/${quotaFor.name}/quota`, { quotaMB: mb })
      notice = t('Quota of {name}: {quota}.', { name: quotaFor.name, quota: quotaText(mb) })
      quotaFor = null
      refresh()
    } catch (e) {
      quotaError = e.message
    }
  }

  async function enableQuotas(volume) {
    error = ''
    try {
      job = await api.post(`/api/volumes/${volume}/quotas`)
    } catch (e) {
      error = e.message
    }
  }

  function askDelete(u) {
    dialog = {
      title: t('Delete user {name}?', { name: u.name }),
      message: u.shares.length
        ? t('{name} loses access to {shares}. Their files stay on the volumes.', { name: u.name, shares: u.shares.join(', ') })
        : t('{name} loses access to all shares. Their files stay on the volumes.', { name: u.name }),
      confirmLabel: t('Delete user'),
      onConfirm: async () => {
        await api.post(`/api/users/${u.name}/delete`)
        dialog = null
        notice = t('User {name} deleted.', { name: u.name })
        refresh()
      },
    }
  }

  const quotaText = (mb) => (mb ? (mb % 1024 === 0 ? `${mb / 1024} GB` : `${mb} MB`) : t('Unlimited'))
  const withQuotas = $derived(quotas.filter((q) => q.enabled))
  // Space used by a user on each volume with quotas.
  const usageOf = (name) =>
    withQuotas.map((q) => ({ volume: q.volume, ...(q.usage.find((u) => u.user === name) ?? { usedKB: 0, limitKB: 0 }) }))

  // Status from the quota usage on any volume with quotas.
  function status(u) {
    const worst = usageOf(u.name).reduce((m, x) => Math.max(m, x.limitKB ? x.usedKB / x.limitKB : 0), 0)
    if (worst >= 1) return ['bad', N_('Over quota')]
    if (worst >= 0.9) return ['warn', N_('Near quota')]
    return ['good', N_('Active')]
  }
</script>

<main class="page">
  <div class="page-head">
    <div>
      <div class="eyebrow">{t('Local accounts · file sharing only')}</div>
      <h1>{t('Users')}</h1>
    </div>
    <div class="actions">{#if !adding}<button onclick={() => (adding = true)}>{t('New user')}</button>{/if}</div>
  </div>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}
  {#if job}<JobPanel {job} onFinished={refresh} onClose={() => (job = null)} />{/if}

  <div class="grid cols-4 kpis">
    <div class="card kpi"><div class="label">{t('Users')}</div><div class="value">{users.length}</div></div>
    <div class="card kpi"><div class="label">{t('With a quota')}</div><div class="value">{users.filter((u) => u.quotaMB).length} <small>/ {users.length}</small></div></div>
    <div class="card kpi"><div class="label">{t('Home folders')}</div><div class="value">{homes?.enabled ? t('On') : t('Off')}</div></div>
    <div class="card kpi" class:warn={security?.failedSignIns24h}><div class="label">{t('Failed sign-ins · 24 h')}</div><div class="value">{security?.failedSignIns24h ?? '–'}</div></div>
  </div>

  {#if adding}
    <form class="card create" onsubmit={createUser}>
      <h2>{t('New user')}</h2>
      <label>
        {t('User name')}
        <!-- svelte-ignore a11y_autofocus -->
        <input bind:value={newName} autocomplete="off" spellcheck="false" maxlength="32" autofocus />
        <span class="hint">{t('Lowercase letters, digits, - and _. This is what they type when connecting.')}</span>
      </label>
      <label>{t('Email')} <span class="muted small">({t('optional')})</span>
        <input type="email" bind:value={newEmail} autocomplete="off" placeholder="alice@example.com" />
      </label>
      <div class="two-cols">
        <label>{t('Password')} <input type="password" bind:value={newPw} autocomplete="new-password" minlength="8" required /></label>
        <label>{t('Confirm password')} <input type="password" bind:value={newPw2} autocomplete="new-password" required /></label>
      </div>
      {#if formError}<p class="error">{formError}</p>{/if}
      <div class="actions">
        <button type="button" class="secondary" onclick={() => (adding = false)}>{t('Cancel')}</button>
        <button type="submit">{t('Create user')}</button>
      </div>
    </form>
  {/if}

  <div class="users-layout">
    <section class="card table-card">
      <div class="card-head"><h2>{t('Users')}</h2></div>
      {#if users.length}
        <table class="data">
          <thead><tr><th>{t('User')}</th><th class="hide-sm">{t('Shares')}</th><th>{t('Quota')}</th><th>{t('Status')}</th><th></th></tr></thead>
          <tbody>
            {#each users as u (u.name)}
              {@const [cls, st] = status(u)}
              <tr>
                <td><div class="who"><span class="avatar">{u.name.slice(0, 2).toUpperCase()}</span><span>{u.name}{#if u.email}<br /><span class="muted small">{u.email}</span>{/if}</span></div></td>
                <td class="hide-sm"><div class="badges">{#each u.shares as sh}<span class="badge">{sh}</span>{:else}<span class="muted small">—</span>{/each}</div></td>
                <td class="quota-cell">
                  {#each usageOf(u.name) as x}
                    {@const pct = x.limitKB ? Math.round((100 * x.usedKB) / x.limitKB) : 0}
                    <div class="usage-line">
                      <span class="small">{formatBytes(x.usedKB * 1024)}{x.limitKB ? ` / ${formatBytes(x.limitKB * 1024)}` : ''}{withQuotas.length > 1 ? ` · ${x.volume}` : ''}</span>
                      {#if x.limitKB}<span class="meter"><span style="width: {Math.min(pct, 100)}%" class:full={pct >= 90}></span></span>{/if}
                    </div>
                  {:else}
                    <span class="small">{quotaText(u.quotaMB)}</span>
                  {/each}
                </td>
                <td><span class="badge {cls}">{t(st)}</span></td>
                <td class="right row-actions">
                  <button class="link" onclick={() => (editing = u)}>{t('Edit')}</button>
                  <button class="link" onclick={() => openPassword(u.name)}>{t('Password')}</button>
                  <button class="link" onclick={() => openQuota(u)}>{t('Quota')}</button>
                  <button class="link danger-text" onclick={() => askDelete(u)}>{t('Delete')}</button>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else if loaded}
        <p class="muted empty-note">{t('No users yet. Create one for each person who will use the NAS.')}</p>
      {/if}
      {#if quotas.length}
        <div class="quota-volumes">
          <span class="muted small">{t('Quotas apply on:')}</span>
          {#each quotas as q (q.volume)}
            {#if q.enabled}
              <span class="badge good">{q.volume}</span>
            {:else if q.filesystem === 'ext4'}
              <span class="small">{t('{volume} (off)', { volume: q.volume })}</span>
              <button class="link small" onclick={() => enableQuotas(q.volume)} disabled={job?.status === 'running'}
                title={t('The volume is unmounted for a few seconds: nobody should be using it')}>{t('Enable')}</button>
            {:else}
              <span class="muted small">{t('{volume} (not available on {filesystem})', { volume: q.volume, filesystem: q.filesystem })}</span>
            {/if}
          {/each}
        </div>
      {/if}
    </section>

    <div class="stack">
      <section class="card">
        <div class="card-head"><h2>{t('Sign-in policy')}</h2></div>
        {#if security}
          <dl class="share-info">
            <dt>{t('Minimum password length')}</dt><dd class="right">{security.minPasswordLength}</dd>
            <dt>{t('Lockout')}</dt><dd class="right">{t('{tries} tries · {minutes} min', { tries: security.lockoutTries, minutes: security.lockoutMinutes })}</dd>
            <dt>{t('Admin session')}</dt><dd class="right">{t('{minutes} min idle', { minutes: security.sessionMinutes })}</dd>
            <dt>{t('SSH for users')}</dt><dd class="right good-text">{t('Refused')}</dd>
          </dl>
        {/if}
      </section>
      <section class="card">
        <div class="card-head"><h2>{t('Administrator')}</h2></div>
        <div class="who"><span class="avatar">AD</span><div><div>admin</div><div class="muted small">{t("Signs in here · can't use shares")}</div></div>
          <span class="spacer"></span><button class="link" onclick={() => openPassword('__admin__')}>{t('Password')}</button></div>
      </section>
    </div>
  </div>
</main>

{#if pwFor}
  <div class="backdrop" role="dialog" aria-modal="true" aria-labelledby="pw-title">
    <form class="card dialog" onsubmit={savePassword}>
      <h2 id="pw-title">{t('Change password: {name}', { name: pwFor === '__admin__' ? 'admin' : pwFor })}</h2>
      {#if pwFor === '__admin__'}
        <label>{t('Current password')} <input type="password" bind:value={current} autocomplete="current-password" required /></label>
      {/if}
      <label>{t('New password')} <input type="password" bind:value={pw} autocomplete="new-password" minlength="8" required /></label>
      <label>{t('Confirm new password')} <input type="password" bind:value={pw2} autocomplete="new-password" required /></label>
      {#if pwError}<p class="error">{pwError}</p>{/if}
      <div class="actions">
        <button type="button" class="secondary" onclick={() => (pwFor = null)}>{t('Cancel')}</button>
        <button type="submit">{t('Change password')}</button>
      </div>
    </form>
  </div>
{/if}

{#if quotaFor}
  <div class="backdrop" role="dialog" aria-modal="true" aria-labelledby="quota-title">
    <form class="card dialog" onsubmit={saveQuota}>
      <h2 id="quota-title">{t('Quota: {name}', { name: quotaFor.name })}</h2>
      <p class="small">{t('Maximum space {name} can fill on each volume, in all shares together. Empty or 0 = unlimited.', { name: quotaFor.name })}</p>
      <div class="add">
        <!-- svelte-ignore a11y_autofocus -->
        <input type="number" min="0" bind:value={quotaFor.value} placeholder={t('Unlimited')} style="width: 9rem" autofocus />
        <select bind:value={quotaFor.unit} aria-label={t('Unit')}><option>GB</option><option>MB</option></select>
      </div>
      {#if !withQuotas.length}<p class="hint">{t('Saved now, enforced once quotas are enabled on a volume.')}</p>{/if}
      {#if quotaError}<p class="error">{quotaError}</p>{/if}
      <div class="actions">
        <button type="button" class="secondary" onclick={() => (quotaFor = null)}>{t('Cancel')}</button>
        <button type="submit">{t('Save')}</button>
      </div>
    </form>
  </div>
{/if}

{#if editing}
  <UserEdit user={editing} onCancel={() => (editing = null)}
    onDone={(name) => { notice = t('User {name} saved.', { name }); editing = null; refresh() }} />
{/if}

{#if dialog}
  <ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />
{/if}

<style>
  .kpis { margin-bottom: 14px; }
  .users-layout { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 14px; align-items: start; }
  .who { display: flex; align-items: center; gap: 0.6rem; }
  .quota-cell { min-width: 9rem; }
  .empty-note { padding: 0 1.25rem 1.25rem; }
  @media (max-width: 1100px) { .users-layout { grid-template-columns: 1fr; } }
</style>
