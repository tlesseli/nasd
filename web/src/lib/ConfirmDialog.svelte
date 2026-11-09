<script>
  import { t } from '../i18n.svelte.js'

  // A modal for destructive actions. With `expected`, the user must type that
  // text (e.g. the volume name) before the button unlocks; with askPassword,
  // the admin password too (checked by the NAS, passed to onConfirm).
  let { title, message, expected = '', askPassword = false, confirmLabel = t('Confirm'), danger = true, onConfirm, onCancel } = $props()

  let typed = $state('')
  let busy = $state(false)
  let error = $state('')
  let password = $state('')
  let ready = $derived((!expected || typed === expected) && (!askPassword || password.length > 0))

  async function confirm(event) {
    event.preventDefault()
    if (!ready) return
    busy = true
    error = ''
    try {
      await onConfirm(password)
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && !busy && onCancel()} />

<div class="backdrop" role="dialog" aria-modal="true" aria-labelledby="dialog-title">
  <form class="card dialog" onsubmit={confirm}>
    <h2 id="dialog-title">{title}</h2>
    <p>{message}</p>
    {#if expected}
      {@const parts = t('Type {text} to confirm').split('{text}')}
      <label>
        <span>{parts[0]}<strong class="mono">{expected}</strong>{parts[1] ?? ''}</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input bind:value={typed} autocomplete="off" spellcheck="false" autofocus />
      </label>
    {/if}
    {#if askPassword}
      <label>
        <span>{t('Admin password')}</span>
        <input type="password" bind:value={password} autocomplete="current-password" />
      </label>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
    <div class="actions">
      <button type="button" class="secondary" onclick={onCancel} disabled={busy}>{t('Cancel')}</button>
      <button type="submit" class:danger disabled={!ready || busy}>{#if busy}<span class="spinner" aria-hidden="true"></span>{t('Starting…')}{:else}{confirmLabel}{/if}</button>
    </div>
  </form>
</div>
