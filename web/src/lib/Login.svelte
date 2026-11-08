<script>
  import { api } from '../api.js'

  // setup=true: first start, choose the admin password.
  let { setup, onAuthenticated } = $props()

  let password = $state('')
  let confirm = $state('')
  let error = $state('')
  let busy = $state(false)

  async function submit(event) {
    event.preventDefault()
    error = ''
    if (setup && password !== confirm) return (error = 'The passwords do not match.')
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
    <h1>{setup ? 'Welcome to your NAS' : 'Sign in'}</h1>
    {#if setup}
      <p class="muted">Choose the password for the <strong>admin</strong> account. You'll use it to manage disks, volumes and shares.</p>
    {/if}

    <label>
      Password
      <!-- svelte-ignore a11y_autofocus -->
      <input type="password" bind:value={password} autocomplete={setup ? 'new-password' : 'current-password'} minlength={setup ? 8 : undefined} required autofocus />
    </label>
    {#if setup}
      <label>
        Confirm password
        <input type="password" bind:value={confirm} autocomplete="new-password" required />
      </label>
    {/if}

    {#if error}<p class="error">{error}</p>{/if}
    <button type="submit" disabled={busy}>{busy ? 'Please wait…' : setup ? 'Create admin account' : 'Sign in'}</button>
  </form>
</main>
