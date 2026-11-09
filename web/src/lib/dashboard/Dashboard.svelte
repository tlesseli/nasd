<script>
  // The Overview page in the style of the interface theme (System Settings →
  // General): Classic, Control panel or Neon.
  import Overview from '../Overview.svelte'
  import ControlPanel from './ControlPanel.svelte'
  import Neon from './Neon.svelte'
  import { look } from '../../theme.svelte.js'

  let { user, onSessionLost, problems = [] } = $props()
</script>

{#if look.theme === 'panel'}
  <main class="page wide"><ControlPanel {onSessionLost} /></main>
{:else if look.theme === 'neon'}
  <main class="page wide"><Neon {user} {onSessionLost} /></main>
{:else}
  <Overview {user} {onSessionLost} {problems} />
{/if}

<style>
  .wide { max-width: 1500px; }
</style>
