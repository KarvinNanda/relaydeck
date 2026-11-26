// The panels draw with xterm's WebGL renderer when the browser has WebGL2.
// The DOM renderer draws every character with the font, so a symbol the
// font lacks comes from a wider fallback font and spills over its neighbors,
// and box drawing breaks between rows (#117, seen on Windows, where the font
// stack falls back to Consolas). The WebGL renderer draws box drawing itself
// and, with rescaleOverlappingGlyphs, scales a wide glyph into its cell.

export interface WebglLike {
  onContextLoss(cb: () => void): unknown
  dispose(): void
}

interface AddonHost {
  loadAddon(addon: never): void
}

// useWebgl loads the addon made by make into term and reports whether it
// runs. Building or loading it throws without WebGL2 (and on Safari < 16);
// xterm then keeps its DOM renderer. When the GPU drops the context later,
// disposing the addon puts xterm back on the DOM renderer, so a panel never
// goes blank.
export function useWebgl(term: AddonHost, make: () => WebglLike): boolean {
  try {
    const addon = make()
    addon.onContextLoss(() => addon.dispose())
    term.loadAddon(addon as never)
    return true
  } catch {
    return false
  }
}
