<script setup lang="ts">
// Terminal text size for every panel, in the top bar. A smaller size shows
// more rows and columns in each panel; the choice is kept per browser.
import { FONT_MAX, FONT_MIN, prefs, setTermFont } from '../stores/prefs'

// aria-disabled instead of disabled at the limits: a disabled button drops
// keyboard focus to the page (#81). The click is ignored there.
function step(d: number): void {
  const next = prefs.termFont + d
  if (next < FONT_MIN || next > FONT_MAX) return
  setTermFont(next)
}
</script>

<template>
  <div class="fs" role="group" aria-label="Terminal text size">
    <button
      class="ib smaller"
      type="button"
      aria-label="Smaller terminal text"
      title="Smaller terminal text"
      :aria-disabled="prefs.termFont <= FONT_MIN ? 'true' : 'false'"
      @click="step(-1)"
    >A<span aria-hidden="true">−</span></button>
    <span class="v" aria-live="polite">{{ prefs.termFont }}px</span>
    <button
      class="ib bigger"
      type="button"
      aria-label="Bigger terminal text"
      title="Bigger terminal text"
      :aria-disabled="prefs.termFont >= FONT_MAX ? 'true' : 'false'"
      @click="step(1)"
    >A<span aria-hidden="true">+</span></button>
  </div>
</template>

<style scoped>
.fs { display: flex; align-items: center; gap: var(--s-1); flex: none; }
/* the same frame as the Messages button next to it, so A- and A+ read as
   buttons at every width (#96) */
.fs .ib {
  min-width: 30px; font-weight: 600;
  border-color: var(--border-strong); background: var(--muted); color: var(--text);
}
.fs .ib:hover:not([aria-disabled="true"]) { border-color: var(--text-muted); }
.fs .ib[aria-disabled="true"] { opacity: .45; cursor: default; }
.v { min-width: 4ch; text-align: center; color: var(--text-muted); font-size: 12px; }
@media (max-width: 767px) {
  /* visible, equal 44px buttons with the size in a fixed slot between them,
     so the control lines up with the 16px gutter like the other buttons */
  .fs { gap: var(--s-3); }
  .fs .ib { width: var(--touch-h); min-width: var(--touch-h); height: var(--touch-h); }
  .v { width: 5ch; }
}
</style>
