import { useEffect } from 'react'

const HIDDEN = 'is-hidden'

/* Secties met [data-reveal] faden in bij het binnenkomen. De klasse
   wordt pas hier gezet, dus zonder JS staat alles gewoon zichtbaar.
   IntersectionObserver vuurt niet in elke embedding-context, vandaar de
   twee vangnetten: een scroll-check en een timer die alles alsnog
   toont. */
export function useReveals() {
  useEffect(() => {
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (reduced || !('IntersectionObserver' in window)) return

    const els = Array.from(document.querySelectorAll<HTMLElement>('[data-reveal]'))
    els.forEach((el) => el.classList.add(HIDDEN))
    const show = (el: HTMLElement) => el.classList.remove(HIDDEN)

    const io = new IntersectionObserver(
      (entries) => {
        entries.forEach((entry) => {
          if (entry.isIntersecting) {
            show(entry.target as HTMLElement)
            io.unobserve(entry.target)
          }
        })
      },
      { rootMargin: '0px 0px -12% 0px' },
    )
    els.forEach((el) => io.observe(el))

    const checkReveal = () => {
      els.forEach((el) => {
        if (el.getBoundingClientRect().top < window.innerHeight * 0.9) show(el)
      })
    }
    window.addEventListener('scroll', checkReveal, { passive: true })
    const firstCheck = window.setTimeout(checkReveal, 400)
    const fallback = window.setTimeout(() => els.forEach(show), 1200)

    return () => {
      io.disconnect()
      window.removeEventListener('scroll', checkReveal)
      window.clearTimeout(firstCheck)
      window.clearTimeout(fallback)
    }
  }, [])
}
