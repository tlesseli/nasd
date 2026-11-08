<script>
  import { onMount } from 'svelte'
  import { api } from './api.js'
  import Login from './lib/Login.svelte'
  import Dashboard from './lib/Dashboard.svelte'

  // Which screen to show: 'loading' | 'setup' | 'login' | 'app'
  let screen = $state('loading')
  let user = $state(null)
  let bootError = $state('')

  onMount(async () => {
    try {
      const { needsSetup } = await api.get('/api/setup')
      if (needsSetup) return (screen = 'setup')
      user = (await api.get('/api/me')).username
      screen = 'app'
    } catch (e) {
      if (e.status === 401) screen = 'login'
      else bootError = e.message
    }
  })

  function onAuthenticated(name) {
    user = name
    screen = 'app'
  }

  async function logout() {
    await api.post('/api/logout')
    user = null
    screen = 'login'
  }
</script>

{#if bootError}
  <main class="center"><p class="error">Cannot reach the NAS: {bootError}</p></main>
{:else if screen === 'setup' || screen === 'login'}
  <Login setup={screen === 'setup'} {onAuthenticated} />
{:else if screen === 'app'}
  <Dashboard {user} {logout} onSessionLost={() => (screen = 'login')} />
{/if}
