<script>
  import { untrack } from 'svelte'
  import { api, formatBytes } from '../api.js'
  import { t, N_ } from '../i18n.svelte.js'

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
    aws: [['DEEP_ARCHIVE', N_('Glacier Deep Archive · cheapest (~1 $/TB/month), retrieval 12-48 h')],
      ['GLACIER', N_('Glacier Flexible Retrieval · retrieval 3-12 h (bulk)')], ['GLACIER_IR', N_('Glacier Instant Retrieval · readable at once')],
      ['STANDARD_IA', N_('Standard-IA · readable at once')], ['STANDARD', N_('Standard')]],
    scaleway: [['GLACIER', N_('Glacier · cheapest, data in Europe, retrieval up to 12 h')], ['ONEZONE_IA', N_('One Zone-IA · readable at once')], ['STANDARD', N_('Standard')]],
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
  const days = [N_('Sunday'), N_('Monday'), N_('Tuesday'), N_('Wednesday'), N_('Thursday'), N_('Friday'), N_('Saturday')]

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
    testResult = { ok: null, text: t('Testing…') }
    try {
      const res = await api.post('/api/backups/test', { id: job?.id ?? 0, ...target() })
      testResult = { ok: true, text: res.message }
    } catch (e) {
      testResult = { ok: false, text: e.message }
    }
  }

  async function submit(event) {
    event.preventDefault()
    if (kind === 's3' && encryptPassword !== encryptPassword2) return (error = t('The encryption passwords do not match.'))
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
  <h2>{editing ? t('Edit {name}', { name: job.name }) : t('New backup job')}</h2>

  <label>
    {t('Name')}
    <input bind:value={name} maxlength="40" placeholder={t('e.g. Photos to USB')} />
  </label>

  <div class="two-cols">
    <label>
      {t('Share')}
      <select bind:value={share}>
        {#each shares as s (s.name)}<option value={s.name}>{s.name}</option>{/each}
      </select>
    </label>
    <label>
      {t('Direction')}
      <select bind:value={direction}>
        <option value="push">{t('Back up the share → to the destination')}</option>
        <option value="pull">{t('Copy from the source → into the share')}</option>
      </select>
    </label>
  </div>

  <fieldset>
    <legend>{direction === 'push' ? t('Destination') : t('Source')}</legend>
    <div class="segmented wide" role="radiogroup" aria-label={t('Kind of target')}>
      {#each [['usb', t('USB disk')], ['ssh', t('Computer (SSH)')], ['rsync', t('rsync server / old NAS')], ['s3', t('Cloud archive (S3)')]] as [val, label]}
        <label class:on={kind === val}><input type="radio" bind:group={kind} value={val} />{label}</label>
      {/each}
    </div>

    {#if kind === 'usb'}
      {#if usbVolumes.length}
        <select bind:value={usbUUID} aria-label={t('USB disk')}>
          <option value="" disabled>{t('Choose a USB disk…')}</option>
          {#each usbVolumes as v (v.uuid)}
            <option value={v.uuid}>{v.label || v.device} · {v.fstype} · {formatBytes(v.sizeBytes)} · {v.model}</option>
          {/each}
        </select>
      {:else}
        <p class="muted small">{t('No formatted USB disk is plugged in. Plug one into the NAS (or format it below the job list).')}</p>
      {/if}
      {#if job?.target.usbUUID && !usbVolumes.some((v) => v.uuid === job.target.usbUUID)}
        <p class="hint">{t('The disk used by this job ({uuid}) is not plugged in right now.', { uuid: job.target.usbUUID })}</p>
      {/if}
      <label>{t('Folder on the disk')} <input bind:value={path} placeholder="nas-backup/{share}" /></label>
      <p class="hint">{t('The disk is mounted only while the job runs: you can unplug it any other time.')}</p>
    {:else if kind === 's3'}
      <div class="two-cols">
        <label>{t('Provider')}
          <select bind:value={provider}>
            <option value="scaleway">Scaleway Object Storage</option>
            <option value="aws">Amazon S3 (AWS)</option>
            <option value="other">{t('Other S3-compatible')}</option>
          </select></label>
        {#if provider === 'other'}
          <label>{t('Endpoint')} <input bind:value={endpoint} placeholder="https://s3.example.com" spellcheck="false" /></label>
          <label>{t('Region')} <input bind:value={region} placeholder="us-east-1" spellcheck="false" /></label>
        {:else}
          <label>{t('Region')} <select bind:value={region}>{#each regions[provider] as r}<option>{r}</option>{/each}</select></label>
        {/if}
      </div>
      <div class="two-cols">
        <label>{t('Bucket')} <input bind:value={bucket} placeholder="my-nas-archive" spellcheck="false" /></label>
        <label>{t('Folder in the bucket')} <span class="hint">{t('(optional)')}</span> <input bind:value={path} placeholder={share} spellcheck="false" /></label>
      </div>
      <div class="two-cols">
        <label>{t('Access key')} <input bind:value={accessKey} autocomplete="off" spellcheck="false" /></label>
        <label>{t('Secret key')} {editing ? t('(empty = keep)') : ''} <input type="password" bind:value={secretKey} autocomplete="new-password" /></label>
      </div>
      <label>{t('Storage class')}
        {#if provider === 'other'}
          <input bind:value={storageClass} placeholder={t('(provider default)')} spellcheck="false" />
        {:else}
          <select bind:value={storageClass}>{#each classes[provider] as [c, label]}<option value={c}>{t(label)}</option>{/each}</select>
        {/if}
      </label>
      {#if archiveClass}
        <p class="hint">{t('Archive class: storing is very cheap, but reading files back needs a retrieval request first (hours), and deleting files within the minimum duration (90-180 days) is billed anyway.')}</p>
      {/if}
      <div class="two-cols">
        <label>{t('Encryption password')} {editing && job?.target.encrypted ? t('(set; empty = keep)') : t('(recommended)')}
          <input type="password" bind:value={encryptPassword} autocomplete="new-password" /></label>
        <label>{t('Confirm')} <input type="password" bind:value={encryptPassword2} autocomplete="new-password" /></label>
      </div>
      <p class="hint"><strong>{t('Files and names are encrypted on the NAS before leaving it:')}</strong> {t("the provider can't read them. Without this password, nobody can, you included: keep it in a password manager.")}</p>
      <label>{t('Upload limit')} <span class="hint">{t('(MB/s, empty = no limit; keeps your internet usable)')}</span>
        <input type="number" min="0" step="0.5" bind:value={bandwidthMB} style="width: 9rem" /></label>
    {:else}
      <div class="two-cols">
        <label>{t('Host')} <input bind:value={host} placeholder="192.168.1.20" autocomplete="off" spellcheck="false" /></label>
        <label>{t('Port')} <input bind:value={port} inputmode="numeric" placeholder={kind === 'ssh' ? '22' : '873'} /></label>
      </div>
      {#if kind === 'ssh'}
        <div class="two-cols">
          <label>{t('User')} <input bind:value={user} autocomplete="off" spellcheck="false" /></label>
          <label>{t('Folder')} <input bind:value={path} placeholder="/backups/nas" spellcheck="false" /></label>
        </div>
        <div class="keybox">
          <p class="small">{t('The NAS logs in with its own SSH key. On the other computer, add this line to')} <span class="mono">~{user || 'user'}/.ssh/authorized_keys</span>:</p>
          <pre>{publicKey || t('loading…')}</pre>
        </div>
      {:else}
        <div class="two-cols">
          <label>{t('Module')} <span class="hint">{t('(on a ReadyNAS: the share name)')}</span> <input bind:value={module} spellcheck="false" /></label>
          <label>{t('Sub-folder')} <span class="hint">{t('(optional)')}</span> <input bind:value={path} spellcheck="false" /></label>
        </div>
        <div class="two-cols">
          <label>{t('User')} <span class="hint">{t('(optional)')}</span> <input bind:value={user} autocomplete="off" spellcheck="false" /></label>
          <label>{t('Password')} <span class="hint">{editing ? t('(leave empty to keep)') : t('(optional)')}</span> <input type="password" bind:value={password} autocomplete="new-password" /></label>
        </div>
      {/if}
    {/if}
    <div class="add">
      <button type="button" class="secondary" onclick={test}>{t('Test connection')}</button>
      {#if testResult}
        <span class={testResult.ok === false ? 'error small' : testResult.ok ? 'good-text small' : 'muted small'}>{testResult.text}</span>
      {/if}
    </div>
  </fieldset>

  <fieldset>
    <legend>{t('Options')}</legend>
    <label class="check">
      <input type="checkbox" bind:checked={mirror} />
      <span><strong>{t('Mirror')}</strong> <span class="muted small">{direction === 'push' ? t('· files deleted from the share are also deleted from the backup. Without it, the backup keeps everything ever copied (safer against accidental deletions).') : t('· files deleted from the source are also deleted from the share. Without it, the backup keeps everything ever copied (safer against accidental deletions).')}</span></span>
    </label>
    <label>
      {t('Exclude')} <span class="hint">{t('(one pattern per line, e.g.')} <span class="mono">*.tmp</span> {t('or')} <span class="mono">Cache/</span>{t('; the recycle bin is always excluded)')}</span>
      <textarea bind:value={excludes} rows="3" spellcheck="false"></textarea>
    </label>
  </fieldset>

  <fieldset>
    <legend>{t('Schedule')}</legend>
    <div class="two-cols">
      <select bind:value={scheduleKind} aria-label={t('Frequency')}>
        <option value="">{t('Manual only')}</option>
        <option value="hourly">{t('Every hour')}</option>
        <option value="daily">{t('Every day')}</option>
        <option value="weekly">{t('Every week')}</option>
      </select>
      {#if scheduleKind === 'weekly'}
        <select bind:value={weekday} aria-label={t('Day')}>
          {#each days as d, i}<option value={i}>{t(d)}</option>{/each}
        </select>
      {/if}
      {#if scheduleKind}
        <input type="time" bind:value={time} aria-label={t('Time')} />
      {/if}
    </div>
    {#if scheduleKind === 'hourly'}<p class="hint">{t('Runs every hour at minute {minute}.', { minute: time.split(':')[1] })}</p>{/if}
  </fieldset>

  {#if error}<p class="error">{error}</p>{/if}
  <div class="actions">
    <button type="button" class="secondary" onclick={onCancel}>{t('Cancel')}</button>
    <button type="submit" disabled={busy || !name || !share}>{busy ? t('Saving…') : editing ? t('Save') : t('Create job')}</button>
  </div>
</form>
