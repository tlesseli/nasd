<script>
  // © line: on the sign-in page and at the bottom of the sidebar.
  /* global __APP_VERSION__ */
  let { light = false } = $props()
</script>

<p class="copyright" class:light>© Ulixes-Consulting · 2026 · v{__APP_VERSION__}</p>

<style>
  .copyright { margin: 0; padding: 0 6px; font-size: 0.72rem; color: var(--muted); }
  .copyright.light { color: rgb(220 232 255 / 0.75); text-shadow: 0 1px 2px #000a; }
</style>
