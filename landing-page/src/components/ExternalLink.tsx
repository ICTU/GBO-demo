import type { ReactNode } from 'react'

type Props = {
  href: string
  /** Waar de link heen gaat, in de aria-label: "opent {describes} in een nieuw tabblad". */
  describes: string
  className?: string
  children: ReactNode
}

export default function ExternalLink({ href, describes, className, children }: Props) {
  return (
    <a
      href={href}
      aria-label={`Externe link: opent ${describes} in een nieuw tabblad`}
      target="_blank"
      rel="noopener noreferrer"
      className={className}
    >
      {children}
    </a>
  )
}
