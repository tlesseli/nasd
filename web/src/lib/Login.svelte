<script>
  import Copyright from './Copyright.svelte'
  import background from '../assets/login-background.jpg'
  import LangPicker from './LangPicker.svelte'
  import { api } from '../api.js'
  import { t } from '../i18n.svelte.js'

  // setup=true: first start, choose the admin password.
  // codeRequired: the setup code shown at the NAS must be given too.
  let { setup, codeRequired = false, onAuthenticated } = $props()

  let password = $state('')
  let confirm = $state('')
  let code = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(event) {
    event.preventDefault()
    error = ''
    if (setup && password !== confirm) return (error = t('The passwords do not match.'))
    busy = true
    try {
      const res = setup
        ? await api.post('/api/setup', { password, code })
        : await api.post('/api/login', { username: 'admin', password })
      onAuthenticated(res.username)
    } catch (e) {
      error = e.message
    } finally {
      busy = false
    }
  }
</script>

<main class="center login-page" style="--bg-image: url({background})">
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

    {#if setup && codeRequired}
      <label>
        {t('Setup code')}
        <input bind:value={code} autocomplete="off" spellcheck="false" placeholder="abcd-efgh" required />
        <span class="hint">{t('Shown on the NAS front display. Without a display: in the system log (journalctl -u nasd).')}</span>
      </label>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
    <button type="submit" disabled={busy}>{busy ? t('Please wait…') : setup ? t('Create admin account') : t('Sign in')}</button>
    {#if !setup}
      <details class="forgot muted small">
        <summary>{t('Forgot the password?')}</summary>
        {t('With a paperclip, hold the Reset button at the back of the NAS for 5 seconds: a new password appears on the front display.')}
      </details>
    {/if}
    <LangPicker />
  </form>
  <footer><Copyright light /></footer>
</main>

<style>
  /* The sign-in card over the server-room picture, readable in every theme. */
  .login-page { position: relative; min-height: 100vh; padding: 16px; flex-direction: column; gap: 1.2rem;
    background: #060a18 var(--bg-image) center / cover no-repeat; }
  .login-page::before { content: ''; position: absolute; inset: 0; background: radial-gradient(ellipse at center, rgb(6 10 24 / 0.25), rgb(6 10 24 / 0.75)); }
  .login-page > * { position: relative; }
  .login-page :global(.login) { background: rgb(11 20 48 / 0.72); border-color: rgb(54 140 255 / 0.35); color: #dce8ff;
    backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px); box-shadow: 0 0 40px rgb(29 78 216 / 0.35); }
  .login-page :global(.login h1) { color: #f2f8ff; background: none; -webkit-background-clip: border-box; background-clip: border-box; }
  .login-page :global(.login .muted), .login-page :global(.login label) { color: #bcd0f0; }
  .login-page :global(.login input), .login-page :global(.login select) { background: rgb(4 8 22 / 0.65); color: #eef4ff; border-color: rgb(54 140 255 / 0.4); }
  footer { position: absolute; bottom: 14px; left: 0; right: 0; text-align: center; }
  .forgot summary { cursor: pointer; }
</style>
