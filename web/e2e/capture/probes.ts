// Page-side probes for the exemplar's seam and text-over-art tools; each runs
// inside page.evaluate, so it must stay self-contained.

/** Text drawn over the splash, as line-box rects and colours for the probe. */
export function photoText() {
  const targets: [string, string][] = [
    ['title', '[data-hero] h1'],
    ['meta', '[data-hero] h1 + p'],
    ['summary', '[data-hero] h1 ~ button > span'],
    ['season chip', '[role="tab"][aria-selected="false"] > span'],
    ['back link', 'main > div button > span'],
  ]
  const out = []
  for (const [label, selector] of targets) {
    for (const el of document.querySelectorAll<HTMLElement>(selector)) {
      if (label === 'back link' && el.textContent !== 'Library') continue
      const range = document.createRange()
      range.selectNodeContents(el)
      const rects = [...range.getClientRects()].filter((q) => q.width > 1 && q.height > 1)
      if (!rects.length) continue
      const box = el.getBoundingClientRect()
      const r = {
        left: Math.max(Math.min(...rects.map((q) => q.left)), box.left),
        top: Math.max(Math.min(...rects.map((q) => q.top)), box.top),
        right: Math.min(Math.max(...rects.map((q) => q.right)), box.right),
        bottom: Math.min(Math.max(...rects.map((q) => q.bottom)), box.bottom),
      }
      if (r.bottom <= 0 || r.top >= innerHeight || r.right <= 0 || r.left >= innerWidth) continue
      const hit = document.elementFromPoint((r.left + r.right) / 2, (r.top + r.bottom) / 2)
      if (!hit || !(el.contains(hit) || hit.contains(el))) continue
      const cs = getComputedStyle(el)
      const color = cs.color.match(/[\d.]+/g)!.map(Number)
      out.push({ label, x: r.left, y: r.top, w: r.right - r.left, h: r.bottom - r.top, color, size: parseFloat(cs.fontSize), weight: Number(cs.fontWeight) })
    }
  }
  return out
}

/** Every content box in view, so the probe measures only the backdrop. */
export function contentBoxes() {
  const vh = innerHeight
  const content: number[][] = []
  const push = (r: DOMRect) => {
    if (r.width > 0 && r.height > 0 && r.bottom > 0 && r.top < vh) {
      content.push([Math.round(r.left), Math.round(r.top), Math.round(r.width), Math.round(r.height)])
    }
  }
  for (const el of document.querySelectorAll('[data-tiles] > *, [data-check-strip], [data-file-row], a, button, input, [role="tab"], img')) {
    if (el.closest('[data-splash]') || el.getAttribute('aria-hidden') === 'true') continue
    push(el.getBoundingClientRect())
  }
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (!n.textContent?.trim() || n.parentElement?.closest('[data-splash]')) continue
    const range = document.createRange()
    range.selectNodeContents(n)
    for (const r of range.getClientRects()) push(r)
  }
  return { viewport: { w: innerWidth, h: vh }, scroll: { y: Math.round(scrollY) }, content }
}
