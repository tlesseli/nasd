<script>
  import LangPicker from './LangPicker.svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'

  // setup=true: first start, choose the admin password.
  let { setup, onAuthenticated } = $props()

  let password = $state('')
  let confirm = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(event) {
    event.preventDefault()
    error = ''
    if (setup && password !== confirm) return (error = t('The passwords do not match.'))
    busy = true
    try {
      const res = setup
        ? await api.post('/api/setup', { password })
        : await api.post('/api/login', { username: 'admin', password })
      onAuthenticated(res.username)
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<main class="center">
  <form class="card login" onsubmit={submit}>
    <h1>{setup ? t('Welcome to your NAS') : t('Sign in')}</h1>
    {#if setup}
      {@const parts = t("Choose the password for the {user} account. You'll use it to manage disks, volumes and shares.").split('{user}')}
      <p class="muted">{parts[0]}<strong>admin</strong>{parts[1] ?? ''}</p>
    {/if}

    <label>
      {t('Password')}
      <!-- svelte-ignore a11y_autofocus -->
      <input type="password" bind:value={password} autocomplete={setup ? 'new-password' : 'current-password'} minlength={setup ? 8 : undefined} required autofocus />
    </label>
    {#if setup}
      <label>
        {t('Confirm password')}
        <input type="password" bind:value={confirm} autocomplete="new-password" required />
      </label>
    {/if}

    {#if error}<p class="error">{error}</p>{/if}
    <button type="submit" disabled={busy}>{busy ? t('Please wait…') : setup ? t('Create admin account') : t('Sign in')}</button>
    <LangPicker />
  </form>
</main>
