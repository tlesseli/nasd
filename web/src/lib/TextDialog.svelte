<script>
  import { t } from '../i18n.svelte.js'

  // A modal that shows a long text (e.g. a SMART report).
  let { title, text, onClose } = $props()
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onClose()} />

<div class="backdrop" role="dialog" aria-modal="true" aria-labelledby="text-dialog-title">
  <div class="card dialog wide">
    <div class="job-head">
      <h2 id="text-dialog-title">{title}</h2>
      <span class="spacer"></span>
      <button class="link" onclick={onClose}>{t('Close')}</button>
    </div>
    <pre class="logs">{text}</pre>
  </div>
</div>
