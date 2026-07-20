type Props = { status: 'active' | 'expired' | 'revoked' }

const LABELS: Record<Props['status'], string> = {
  active: 'Actief',
  expired: 'Verlopen',
  revoked: 'Ingetrokken',
}

export default function StatusChip({ status }: Props) {
  return <span className={`status-chip ${status}`}>{LABELS[status]}</span>
}
