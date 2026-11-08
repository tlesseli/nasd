<script>
  import { onMount } from 'svelte'
  import { api } from '../api.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import HomesQuotas from './HomesQuotas.svelte'

  let { onSessionLost } = $props()

  let users = $state([])
  let loaded = $state(false)
  let error = $state('')
  let notice = $state('')
  let dialog = $state(null)

  // New user form
  let adding = $state(false)
  let newName = $state('')
  let newPw = $state('')
  let newPw2 = $state('')
  let formError = $state('')

  // Password change (for a NAS user or for admin)
  let pwFor = $state(null) // user name, or '__admin__'
  let current = $state('')
  let pw = $state('')
  let pw2 = $state('')
  let pwError = $state('')

  async function refresh() {
    try {
      users = await api.get('/api/users')
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
    if (newPw !== newPw2) return (formError = 'The passwords do not match.')
    try {
      await api.post('/api/users', { name: newName, password: newPw })
      notice = `User ${newName} created. Give them access to shares on the Shares page.`
      adding = false
      newName = newPw = newPw2 = ''
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
    if (pw !== pw2) return (pwError = 'The passwords do not match.')
    try {
      if (pwFor === '__admin__') await api.post('/api/me/password', { current, new: pw })
      else await api.post(`/api/users/${pwFor}/password`, { password: pw })
      notice = pwFor === '__admin__' ? 'Admin password changed. Other browsers were signed out.' : `Password of ${pwFor} changed.`
      pwFor = null
    } catch (e) {
      pwError = e.message
    }
  }

  function askDelete(u) {
    dialog = {
      title: `Delete user ${u.name}?`,
      message: `${u.name} loses access to ${u.shares.length ? u.shares.join(', ') : 'all shares'}. Their files stay on the volumes.`,
      confirmLabel: 'Delete user',
      onConfirm: async () => {
        await api.post(`/api/users/${u.name}/delete`)
        dialog = null
        notice = `User ${u.name} deleted.`
        refresh()
      },
    }
  }
</script>

<main class="page">
  <div class="page-head">
    <h1>Users</h1>
    <span class="spacer"></span>
    {#if !adding}<button onclick={() => (adding = true)}>New user</button>{/if}
  </div>
  <p class="muted">These accounts connect to shares from computers and phones. They can't sign in to this admin interface.</p>

  {#if error}<p class="error">{error}</p>{/if}
  {#if notice}<p class="notice">{notice}</p>{/if}

  {#if adding}
    <form class="card create" onsubmit={createUser}>
      <h2>New user</h2>
      <label>
        User name
        <!-- svelte-ignore a11y_autofocus -->
        <input bind:value={newName} autocomplete="off" spellcheck="false" maxlength="32" autofocus />
        <span class="hint">Lowercase letters, digits, - and _. This is what they type when connecting.</span>
      </label>
      <label>Password <input type="password" bind:value={newPw} autocomplete="new-password" minlength="8" required /></label>
      <label>Confirm password <input type="password" bind:value={newPw2} autocomplete="new-password" required /></label>
      {#if formError}<p class="error">{formError}</p>{/if}
      <div class="actions">
        <button type="button" class="secondary" onclick={() => (adding = false)}>Cancel</button>
        <button type="submit">Create user</button>
      </div>
    </form>
  {/if}

  <div class="card list">
    {#each users as u (u.name)}
      <div class="row">
        <strong>{u.name}</strong>
        <span class="muted small">{u.shares.length ? `Access to ${u.shares.join(', ')}` : 'No shares yet'}</span>
        <span class="spacer"></span>
        <button class="link" onclick={() => openPassword(u.name)}>Change password</button>
        <button class="link danger-text" onclick={() => askDelete(u)}>Delete</button>
      </div>
    {:else}
      {#if loaded}<p class="muted">No users yet. Create one for each person who will use the NAS.</p>{/if}
    {/each}
  </div>

  {#if loaded}<HomesQuotas {users} onChanged={refresh} />{/if}

  <h2>Administrator</h2>
  <div class="card list">
    <div class="row">
      <strong>admin</strong>
      <span class="muted small">Signs in to this interface. Can't connect to shares.</span>
      <span class="spacer"></span>
      <button class="link" onclick={() => openPassword('__admin__')}>Change password</button>
    </div>
  </div>
</main>

{#if pwFor}
  <div class="backdrop">
    <form class="card dialog" onsubmit={savePassword}>
      <h2>Change password: {pwFor === '__admin__' ? 'admin' : pwFor}</h2>
      {#if pwFor === '__admin__'}
        <label>Current password <input type="password" bind:value={current} autocomplete="current-password" required /></label>
      {/if}
      <label>New password <input type="password" bind:value={pw} autocomplete="new-password" minlength="8" required /></label>
      <label>Confirm new password <input type="password" bind:value={pw2} autocomplete="new-password" required /></label>
      {#if pwError}<p class="error">{pwError}</p>{/if}
      <div class="actions">
        <button type="button" class="secondary" onclick={() => (pwFor = null)}>Cancel</button>
        <button type="submit">Change password</button>
      </div>
    </form>
  </div>
{/if}

{#if dialog}
  <ConfirmDialog {...dialog} onCancel={() => (dialog = null)} />
{/if}
