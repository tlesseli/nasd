<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  import Login from './lib/Login.svelte'
  import Overview from './lib/Overview.svelte'
  import Volumes from './lib/Volumes.svelte'
  import Shares from './lib/Shares.svelte'
  import Users from './lib/Users.svelte'
  import Backups from './lib/Backups.svelte'

  // Which screen to show: 'loading' | 'setup' | 'login' | 'app'
  let screen = $state('loading')
  let user = $state(null)
  let hostname = $state('NAS')
  let bootError = $state('')

  // Minimal hash router (#/, #/volumes, ...). No library needed for a few pages.
  let route = $state(location.hash.slice(1) || '/')
  const pages = [
    { path: '/', label: 'Overview' },
    { path: '/volumes', label: 'Volumes' },
    { path: '/shares', label: 'Shares' },
    { path: '/users', label: 'Users' },
    { path: '/backups', label: 'Backups' },
  ]

  onMount(async () => {
    window.addEventListener('hashchange', () => (route = location.hash.slice(1) || '/'))
    try {
      const { needsSetup } = await api.get('/api/setup')
      if (needsSetup) return (screen = 'setup')
      onAuthenticated((await api.get('/api/me')).username)
    } catch (e) {
      if (e.status === 401) screen = 'login'
      else bootError = e.message
    }
  })

  async function onAuthenticated(name) {
    user = name
    screen = 'app'
    hostname = (await api.get('/api/system').catch(() => ({}))).hostname ?? 'NAS'
  }

  async function logout() {
    await api.post('/api/logout')
    user = null
    screen = 'login'
  }

  const onSessionLost = () => (screen = 'login')
</script>

{#if bootError}
  <main class="center"><p class="error">Cannot reach the NAS: {bootError}</p></main>
{:else if screen === 'setup' || screen === 'login'}
  <Login setup={screen === 'setup'} {onAuthenticated} />
{:else if screen === 'app'}
  <header class="topbar">
    <span class="brand">{hostname}</span>
    <nav>
      {#each pages as p}
        <a href="#{p.path}" class:active={route === p.path}>{p.label}</a>
      {/each}
    </nav>
    <span class="spacer"></span>
    <span class="muted">{user}</span>
    <button class="link" onclick={logout}>Sign out</button>
  </header>

  {#if route === '/volumes'}
    <Volumes {onSessionLost} />
  {:else if route === '/shares'}
    <Shares {hostname} {onSessionLost} />
  {:else if route === '/users'}
    <Users {onSessionLost} />
  {:else if route === '/backups'}
    <Backups {onSessionLost} />
  {:else}
    <Overview {onSessionLost} />
  {/if}
{/if}
