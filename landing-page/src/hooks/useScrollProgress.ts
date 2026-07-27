import { useEffect, useRef } from 'react'

/* Voortgangsbalk bovenaan. De breedte gaat rechtstreeks op het element
   in plaats van door state: scroll-events zijn te frequent om er de
   hele pagina op te laten renderen. */
export function useScrollProgress() {
  const barRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onScroll = () => {
      const max = document.documentElement.scrollHeight - window.innerHeight
      const ratio = max > 0 ? Math.min(1, window.scrollY / max) : 0
      if (barRef.current) barRef.current.style.width = `${ratio * 100}%`
    }
    window.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
    return () => window.removeEventListener('scroll', onScroll)
  }, [])

  return barRef
}
