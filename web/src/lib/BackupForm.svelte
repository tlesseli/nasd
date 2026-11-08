<script>
  import { untrack } from 'svelte'
  import { api, formatBytes } from '../api.js'

  // Create or edit a backup job. `job` is null for a new one.
  let { job: jobProp = null, shares: sharesProp, usb, publicKey, onSave, onCancel } = $props()
  const { job, shares } = untrack(() => ({ job: jobProp, shares: sharesProp }))
  const editing = !!job

  let name = $state(job?.name ?? '')
  let direction = $state(job?.direction ?? 'push')
  let share = $state(job?.share ?? shares[0]?.name ?? '')
  let kind = $state(job?.target.kind ?? 'usb')
  let usbUUID = $state(job?.target.usbUUID ?? '')
  let host = $state(job?.target.host ?? '')
  let port = $state(job?.target.port || '')
  let user = $state(job?.target.user ?? '')
  let password = $state('') // never sent back by the server; empty = keep
  let module = $state(job?.target.module ?? '')
  let path = $state(job?.target.path ?? '')
  let mirror = $state(job?.mirror ?? false)
  // cloud archive (S3)
  const t0 = job?.target ?? {}
  let provider = $state(t0.provider ?? 'scaleway')
  let region = $state(t0.region ?? 'fr-par')
  let endpoint = $state(t0.endpoint ?? '')
  let bucket = $state(t0.bucket ?? '')
  let accessKey = $state(t0.accessKey ?? '')
  let secretKey = $state('')
  let storageClass = $state(t0.storageClass ?? '')
  let encryptPassword = $state('')
  let encryptPassword2 = $state('')
  let bandwidthMB = $state(t0.bandwidthKB ? t0.bandwidthKB / 1024 : '')
  const regions = {
    aws: ['eu-west-3', 'eu-central-1', 'eu-west-1', 'eu-north-1', 'us-east-1', 'us-west-2'],
    scaleway: ['fr-par', 'nl-ams', 'pl-waw'],
  }
  const classes = {
    aws: [['DEEP_ARCHIVE', 'Glacier Deep Archive · cheapest (~1 $/TB/month), retrieval 12-48 h'],
      ['GLACIER', 'Glacier Flexible Retrieval · retrieval 3-12 h (bulk)'], ['GLACIER_IR', 'Glacier Instant Retrieval · readable at once'],
      ['STANDARD_IA', 'Standard-IA · readable at once'], ['STANDARD', 'Standard']],
    scaleway: [['GLACIER', 'Glacier · cheapest, data in Europe, retrieval up to 12 h'], ['ONEZONE_IA', 'One Zone-IA · readable at once'], ['STANDARD', 'Standard']],
  }
  $effect(() => {
    // Sensible defaults when switching provider.
    if (provider !== 'other' && !regions[provider].includes(region)) region = regions[provider][0]
    if (provider !== 'other' && !classes[provider].some(([c]) => c === storageClass)) storageClass = classes[provider][0][0]
  })
  const archiveClass = $derived(storageClass === 'DEEP_ARCHIVE' || storageClass === 'GLACIER')
  let excludes = $state((job?.excludes ?? []).join('\n'))
  let scheduleKind = $state(job?.schedule.kind ?? 'daily')
  let weekday = $state(job?.schedule.weekday ?? 0)
  let time = $state(job ? `${String(job.schedule.hour).padStart(2, '0')}:${String(job.schedule.minute).padStart(2, '0')}` : '03:00')

  let busy = $state(false)
  let error = $state('')
  let testResult = $state(null) // {ok, text}

  const usbVolumes = $derived(usb.flatMap((d) => d.volumes))
  const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

  function target() {
    if (kind === 's3') {
      return { kind, provider, region, endpoint, bucket, path, accessKey, secretKey, storageClass,
        encryptPassword, bandwidthKB: Math.round((Number(bandwidthMB) || 0) * 1024) }
    }
    return { kind, usbUUID, host, port: Number(port) || 0, user, password, module, path }
  }

  function payload() {
    const [hour, minute] = time.split(':').map(Number)
    return {
      id: job?.id ?? 0, name, direction, share, target: target(), mirror,
      excludes: excludes.split('\n').map((e) => e.trim()).filter(Boolean),
      schedule: { kind: scheduleKind, weekday: Number(weekday), hour: hour || 0, minute: minute || 0 },
    }
  }

  async function test() {
    testResult = { ok: null, text: 'Testing…' }
    try {
      const res = await api.post('/api/backups/test', { id: job?.id ?? 0, ...target() })
      testResult = { ok: true, text: res.message }
    } catch (e) {
      testResult = { ok: false, text: e.message }
    }
  }

  async function submit(event) {
    event.preventDefault()
    if (kind === 's3' && encryptPassword !== encryptPassword2) return (error = 'The encryption passwords do not match.')
    busy = true
    error = ''
    try {
      await onSave(payload())
    } catch (e) {
      error = e.message
      busy = false
    }
  }
</script>

<form class="card create" onsubmit={submit}>
  <h2>{editing ? `Edit ${job.name}` : 'New backup job'}</h2>

  <label>
    Name
    <input bind:value={name} maxlength="40" placeholder="e.g. Photos to USB" />
  </label>

  <div class="two-cols">
    <label>
      Share
      <select bind:value={share}>
        {#each shares as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
      </select>
    </label>
    <label>
      Direction
      <select bind:value={direction}>
        <option value="push">Back up the share → to the destination</option>
        <option value="pull">Copy from the source → into the share</option>
      </select>
    </label>
  </div>

  <fieldset>
    <legend>{direction === 'push' ? 'Destination' : 'Source'}</legend>
    <div class="segmented wide" role="radiogroup" aria-label="Kind of target">
      {#each [['usb', 'USB disk'], ['ssh', 'Computer (SSH)'], ['rsync', 'rsync server / old NAS'], ['s3', 'Cloud archive (S3)']] as [val, label]}
        <label class:on={kind === val}><input type="radio" bind:group={kind} value={val} />{label}</label>
      {/each}
    </div>

    {#if kind === 'usb'}
      {#if usbVolumes.length}
        <select bind:value={usbUUID} aria-label="USB disk">
          <option value="" disabled>Choose a USB disk…</option>
          {#each usbVolumes as v (v.uuid)}
            <option value={v.uuid}>{v.label || v.device} · {v.fstype} · {formatBytes(v.sizeBytes)} · {v.model}</option>
          {/each}
        </select>
      {:else}
        <p class="muted small">No formatted USB disk is plugged in. Plug one into the NAS (or format it below the job list).</p>
      {/if}
      {#if job?.target.usbUUID && !usbVolumes.some((v) => v.uuid === job.target.usbUUID)}
        <p class="hint">The disk used by this job ({job.target.usbUUID}) is not plugged in right now.</p>
      {/if}
      <label>Folder on the disk <input bind:value={path} placeholder="nas-backup/{share}" /></label>
      <p class="hint">The disk is mounted only while the job runs: you can unplug it any other time.</p>
    {:else if kind === 's3'}
      <div class="two-cols">
        <label>Provider
          <select bind:value={provider}>
            <option value="scaleway">Scaleway Object Storage</option>
            <option value="aws">Amazon S3 (AWS)</option>
            <option value="other">Other S3-compatible</option>
          </select></label>
        {#if provider === 'other'}
          <label>Endpoint <input bind:value={endpoint} placeholder="https://s3.example.com" spellcheck="false" /></label>
          <label>Region <input bind:value={region} placeholder="us-east-1" spellcheck="false" /></label>
        {:else}
          <label>Region <select bind:value={region}>{#each regions[provider] as r}<option>{r}</option>{/each}</select></label>
        {/if}
      </div>
      <div class="two-cols">
        <label>Bucket <input bind:value={bucket} placeholder="my-nas-archive" spellcheck="false" /></label>
        <label>Folder in the bucket <span class="hint">(optional)</span> <input bind:value={path} placeholder={share} spellcheck="false" /></label>
      </div>
      <div class="two-cols">
        <label>Access key <input bind:value={accessKey} autocomplete="off" spellcheck="false" /></label>
        <label>Secret key {editing ? '(empty = keep)' : ''} <input type="password" bind:value={secretKey} autocomplete="new-password" /></label>
      </div>
      <label>Storage class
        {#if provider === 'other'}
          <input bind:value={storageClass} placeholder="(provider default)" spellcheck="false" />
        {:else}
          <select bind:value={storageClass}>{#each classes[provider] as [c, label]}<option value={c}>{label}</option>{/each}</select>
        {/if}
      </label>
      {#if archiveClass}
        <p class="hint">Archive class: storing is very cheap, but reading files back needs a retrieval request first (hours), and deleting files within the minimum duration (90-180 days) is billed anyway.</p>
      {/if}
      <div class="two-cols">
        <label>Encryption password {editing && job?.target.encrypted ? '(set; empty = keep)' : '(recommended)'}
          <input type="password" bind:value={encryptPassword} autocomplete="new-password" /></label>
        <label>Confirm <input type="password" bind:value={encryptPassword2} autocomplete="new-password" /></label>
      </div>
      <p class="hint"><strong>Files and names are encrypted on the NAS before leaving it:</strong> the provider can't read them. Without this password, nobody can, you included: keep it in a password manager.</p>
      <label>Upload limit <span class="hint">(MB/s, empty = no limit; keeps your internet usable)</span>
        <input type="number" min="0" step="0.5" bind:value={bandwidthMB} style="width: 9rem" /></label>
    {:else}
      <div class="two-cols">
        <label>Host <input bind:value={host} placeholder="192.168.1.20" autocomplete="off" spellcheck="false" /></label>
        <label>Port <input bind:value={port} inputmode="numeric" placeholder={kind === 'ssh' ? '22' : '873'} /></label>
      </div>
      {#if kind === 'ssh'}
        <div class="two-cols">
          <label>User <input bind:value={user} autocomplete="off" spellcheck="false" /></label>
          <label>Folder <input bind:value={path} placeholder="/backups/nas" spellcheck="false" /></label>
        </div>
        <div class="keybox">
          <p class="small">The NAS logs in with its own SSH key. On the other computer, add this line to <span class="mono">~{user || 'user'}/.ssh/authorized_keys</span>:</p>
          <pre>{publicKey || 'loading…'}</pre>
        </div>
      {:else}
        <div class="two-cols">
          <label>Module <span class="hint">(on a ReadyNAS: the share name)</span> <input bind:value={module} spellcheck="false" /></label>
          <label>Sub-folder <span class="hint">(optional)</span> <input bind:value={path} spellcheck="false" /></label>
        </div>
        <div class="two-cols">
          <label>User <span class="hint">(optional)</span> <input bind:value={user} autocomplete="off" spellcheck="false" /></label>
          <label>Password <span class="hint">{editing ? '(leave empty to keep)' : '(optional)'}</span> <input type="password" bind:value={password} autocomplete="new-password" /></label>
        </div>
      {/if}
    {/if}
    <div class="add">
      <button type="button" class="secondary" onclick={test}>Test connection</button>
      {#if testResult}
        <span class={testResult.ok === false ? 'error small' : testResult.ok ? 'good-text small' : 'muted small'}>{testResult.text}</span>
      {/if}
    </div>
  </fieldset>

  <fieldset>
    <legend>Options</legend>
    <label class="check">
      <input type="checkbox" bind:checked={mirror} />
      <span><strong>Mirror</strong> <span class="muted small">· files deleted from the {direction === 'push' ? 'share' : 'source'} are also deleted from the {direction === 'push' ? 'backup' : 'share'}. Without it, the backup keeps everything ever copied (safer against accidental deletions).</span></span>
    </label>
    <label>
      Exclude <span class="hint">(one pattern per line, e.g. <span class="mono">*.tmp</span> or <span class="mono">Cache/</span>; the recycle bin is always excluded)</span>
      <textarea bind:value={excludes} rows="3" spellcheck="false"></textarea>
    </label>
  </fieldset>

  <fieldset>
    <legend>Schedule</legend>
    <div class="two-cols">
      <select bind:value={scheduleKind} aria-label="Frequency">
        <option value="">Manual only</option>
        <option value="hourly">Every hour</option>
        <option value="daily">Every day</option>
        <option value="weekly">Every week</option>
      </select>
      {#if scheduleKind === 'weekly'}
        <select bind:value={weekday} aria-label="Day">
          {#each days as d, i}<option value={i}>{d}</option>{/each}
        </select>
      {/if}
      {#if scheduleKind}
        <input type="time" bind:value={time} aria-label="Time" />
      {/if}
    </div>
    {#if scheduleKind === 'hourly'}<p class="hint">Runs every hour at minute {time.split(':')[1]}.</p>{/if}
  </fieldset>

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="secondary" onclick={onCancel}>Cancel</button>
    <button type="submit" disabled={busy || !name || !share}>{busy ? 'Saving…' : editing ? 'Save' : 'Create job'}</button>
  </div>
</form>
