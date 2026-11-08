<script>
  // A modal for destructive actions. With `expected`, the user must type that
  // text (e.g. the volume name) before the button unlocks.
  let { title, message, expected = '', confirmLabel = 'Confirm', danger = true, onConfirm, onCancel } = $props()

  let typed = $state('')
  let busy = $state(false)
  let error = $state('')
  let ready = $derived(!expected || typed === expected)

  async function confirm(event) {
    event.preventDefault()
    if (!ready) return
    busy = true
    error = ''
    try {
      await onConfirm()
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && !busy && onCancel()} />

<div class="backdrop">
  <form class="card dialog" onsubmit={confirm} role="dialog" aria-modal="true" aria-labelledby="dialog-title">
    <h2 id="dialog-title">{title}</h2>
    <p>{message}</p>
    {#if expected}
      <label>
        <span>Type <strong class="mono">{expected}</strong> to confirm</span>
        <!-- svelte-ignore a11y_autofocus -->
        <input bind:value={typed} autocomplete="off" spellcheck="false" autofocus />
      </label>
    {/if}
    {#if error}<p class="error">{error}</p>{/if}
    <div class="actions">
      <button type="button" class="secondary" onclick={onCancel} disabled={busy}>Cancel</button>
      <button type="submit" class:danger disabled={!ready || busy}>{busy ? 'Starting…' : confirmLabel}</button>
    </div>
  </form>
</div>
