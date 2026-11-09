<script>
  import { api } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

  // Shows a background job and its log, polling until it finishes.
  let { job, onFinished, onClose } = $props()

  let current = $state(null)
  let logEl = $state()

  $effect(() => {
    current = job
    if (job.status !== 'running') return
    const timer = setInterval(async () => {
      try {
        current = await api.get(`/api/jobs/${job.id}`)
        if (current.status !== 'running') {
          clearInterval(timer)
          onFinished()
        }
      } catch {
        // NAS busy or briefly unreachable: try again on the next tick
      }
    }, 1000)
    return () => clearInterval(timer)
  })

  // Keep the newest log line visible.
  $effect(() => {
    current?.log?.length
    if (logEl) logEl.scrollTop = logEl.scrollHeight
  })

  const labels = { running: N_('Running'), done: N_('Done'), failed: N_('Failed') }
  const classes = { running: 'info', done: 'good', failed: 'bad' }
</script>

{#if current}
  <section class="card job">
    <div class="job-head">
      <strong>{current.title}</strong>
      <span class="badge {classes[current.status]}">{labels[current.status] ? t(labels[current.status]) : labels[current.status]}</span>
      <span class="spacer"></span>
      {#if current.status !== 'running'}
        <button class="link" onclick={onClose}>{t('Close')}</button>
      {/if}
    </div>
    {#if current.error}<p class="error">{current.error}</p>{/if}
    <pre bind:this={logEl}>{current.log?.join('\n') || t('Starting…')}</pre>
  </section>
{/if}
