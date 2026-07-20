type Props = { title?: string }

export default function BeslissingNodig({ title = 'Beslissing nodig — open ontwerpvraag.' }: Props) {
  return <span className="bn-badge" title={title}>beslissing nodig</span>
}
