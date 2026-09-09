import { useEffect, useRef, useState } from 'react'

export type Size = { width: number; height: number }

/* Meet een element in echte pixels. Nodig voor tekeningen die hun viewBox
   1:1 op hun eigen vak willen leggen: zonder dat schaalt de SVG horizontaal
   en verticaal verschillend, en dan smeert een streepjes-animatie uit op de
   steile stukken van een curve.

   Zonder ResizeObserver (of vóór de eerste meting) blijft de maat 0; de
   aanroeper hoort dan niets te tekenen. Het gaat om decoratie, dus één
   frame zonder tekening is onzichtbaar. */
export function useElementSize<T extends Element>() {
  const ref = useRef<T>(null)
  const [size, setSize] = useState<Size>({ width: 0, height: 0 })

  useEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return

    const measure = () => {
      const { width, height } = el.getBoundingClientRect()
      /* Alleen bijwerken bij een echte verandering: setState met een nieuw
         object zou anders elke observer-callback een render kosten. */
      setSize((prev) =>
        Math.abs(prev.width - width) < 0.5 && Math.abs(prev.height - height) < 0.5
          ? prev
          : { width, height },
      )
    }

    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  return [ref, size] as const
}
