import { useEffect, useState } from 'react'

type Theme = 'dark' | 'light'

const KEY = 'gbo.devp.theme'

function read(): Theme {
  try {
    const v = localStorage.getItem(KEY)
    if (v === 'light' || v === 'dark') return v
  } catch {
    // ignore
  }
  return 'dark'
}

function apply(theme: Theme) {
  document.documentElement.setAttribute('data-theme', theme)
}

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(() => {
    const t = read()
    apply(t)
    return t
  })

  useEffect(() => { apply(theme) }, [theme])

  const setTheme = (t: Theme) => {
    setThemeState(t)
    try { localStorage.setItem(KEY, t) } catch { /* ignore */ }
  }
  const toggle = () => setTheme(theme === 'dark' ? 'light' : 'dark')

  return { theme, setTheme, toggle }
}
