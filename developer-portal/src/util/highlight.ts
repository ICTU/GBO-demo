// Lightweight client-side syntax-highlighting for JSON and GraphQL — no
// editor-library. HTML output uses span.sx-* classes from tokens.css.

const esc = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

export function hlJSON(value: unknown): string {
  let text: string
  try {
    text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  } catch {
    text = String(value)
  }
  if (!text) return ''
  return esc(text)
    .replace(/("(?:\\.|[^"\\])*")(\s*:)?/g, (_m, str, colon) =>
      colon
        ? `<span class="sx-key">${str}</span>${colon}`
        : `<span class="sx-str">${str}</span>`,
    )
    .replace(/\b(true|false|null)\b/g, '<span class="sx-lit">$1</span>')
    .replace(/\b(-?\d+(?:\.\d+)?(?:e[+-]?\d+)?)\b/gi, '<span class="sx-num">$1</span>')
    .replace(/([{}\[\],])/g, '<span class="sx-punc">$1</span>')
}

export function hlGraphQL(text: string): string {
  return esc(text)
    .replace(/\b(query|mutation|subscription|fragment|on)\b/g, '<span class="sx-kw">$1</span>')
    .replace(/(\b[A-Za-z_][\w]*)(\s*\()/g, '<span class="sx-key">$1</span>$2')
    .replace(/("(?:\\.|[^"\\])*")/g, '<span class="sx-str">$1</span>')
    .replace(/\b(true|false|null)\b/g, '<span class="sx-lit">$1</span>')
    .replace(/\b(-?\d+(?:\.\d+)?)\b/g, '<span class="sx-num">$1</span>')
    .replace(/([{}\[\](),:])/g, '<span class="sx-punc">$1</span>')
}
