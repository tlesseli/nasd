<script>
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'

  // Edit a NAS user in one place: name, email, password, quota. Only what
  // changed is sent (renaming keeps the password, files and share access).
  let { user, onDone, onCancel } = $props()

  const gb0 = user.quotaMB >= 1024 && user.quotaMB % 1024 === 0
  let name = $state(user.name)
  let email = $state(user.email ?? '')
  let pw = $state('')
  let pw2 = $state('')
  let quota = $state(user.quotaMB ? (gb0 ? user.quotaMB / 1024 : user.quotaMB) : '')
  let unit = $state(gb0 || !user.quotaMB ? 'GB' : 'MB')
  let busy = $state(false)
  let error = $state('')

  async function save(event) {
    event.preventDefault()
    error = ''
    if (pw && pw !== pw2) return (error = t('The passwords do not match.'))
    busy = true
    let current = user.name
    try {
      if (name !== user.name || email !== (user.email ?? '')) {
        await api.post(`/api/users/${current}`, { name, email })
        current = name
      }
      if (pw) await api.post(`/api/users/${current}/password`, { password: pw })
      const mb = Math.round((Number(quota) || 0) * (unit === 'GB' ? 1024 : 1))
      if (mb !== (user.quotaMB || 0)) await api.post(`/api/users/${current}/quota`, { quotaMB: mb })
      onDone(current)
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<div class="backdrop" role="dialog" aria-modal="true" aria-labelledby="edit-title">
  <form class="card dialog edit" onsubmit={save}>
    <h2 id="edit-title">{t('Edit user {name}', { name: user.name })}</h2>
    <label>{t('User name')}
      <input bind:value={name} autocomplete="off" spellcheck="false" maxlength="32" required />
      {#if name !== user.name}<span class="hint">{t('They will connect with the new name. Their password, files and share access stay the same.')}</span>{/if}
    </label>
    <label>{t('Email')} <span class="muted small">({t('optional')})</span>
      <input type="email" bind:value={email} autocomplete="off" placeholder="alice@example.com" />
    </label>
    <div class="two-cols">
      <label>{t('New password')} <input type="password" bind:value={pw} autocomplete="new-password" minlength="8" placeholder={t('unchanged')} /></label>
      <label>{t('Confirm password')} <input type="password" bind:value={pw2} autocomplete="new-password" disabled={!pw} /></label>
    </div>
    <div>
      <span class="label-text">{t('Quota')}</span>
      <div class="add">
        <input type="number" min="0" bind:value={quota} placeholder={t('Unlimited')} style="width: 9rem" aria-label={t('Quota')} />
        <select bind:value={unit} aria-label={t('Unit')}><option>GB</option><option>MB</option></select>
      </div>
    </div>
    {#if error}<p class="error">{error}</p>{/if}
    <div class="actions">
      <button type="button" class="secondary" onclick={onCancel} disabled={busy}>{t('Cancel')}</button>
      <button type="submit" disabled={busy || !name}>{#if busy}<span class="spinner" aria-hidden="true"></span>{t('Saving…')}{:else}{t('Save')}{/if}</button>
    </div>
  </form>
</div>

<style>
  .edit { display: flex; flex-direction: column; gap: 0.9rem; width: min(520px, calc(100vw - 32px)); }
  .label-text { display: block; margin-bottom: 0.3rem; }
</style>
