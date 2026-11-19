<script setup lang="ts">
// Terminal text size for every panel, in the top bar. A smaller size shows
// more rows and columns in each panel; the choice is kept per browser.
import { FONT_MAX, FONT_MIN, prefs, setTermFont } from '../stores/prefs'
</script>

<template>
  <div class="fs" role="group" aria-label="Terminal text size">
    <button
      class="ib smaller"
      type="button"
      :aria-label="`Smaller terminal text, now ${prefs.termFont}px`"
      title="Smaller terminal text"
      :disabled="prefs.termFont <= FONT_MIN"
      @click="setTermFont(prefs.termFont - 1)"
    >A<span aria-hidden="true">−</span></button>
    <span class="v" aria-hidden="true">{{ prefs.termFont }}px</span>
    <button
      class="ib bigger"
      type="button"
      :aria-label="`Bigger terminal text, now ${prefs.termFont}px`"
      title="Bigger terminal text"
      :disabled="prefs.termFont >= FONT_MAX"
      @click="setTermFont(prefs.termFont + 1)"
    >A<span aria-hidden="true">+</span></button>
  </div>
</template>

<style scoped>
.fs { display: flex; align-items: center; gap: var(--s-1); flex: none; }
.fs .ib { min-width: 30px; font-weight: 600; }
.fs .smaller { font-size: 11px; }
.v { min-width: 4ch; text-align: center; color: var(--text-muted); font-size: 12px; }
@media (max-width: 767px) {
  .fs .ib { min-width: var(--touch-h); height: var(--touch-h); }
}
</style>
