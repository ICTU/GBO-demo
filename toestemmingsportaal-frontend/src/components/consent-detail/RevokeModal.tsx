type Props = {
  afnemer: string
  onCancel: () => void
  onConfirm: () => void
  busy?: boolean
}

export default function RevokeModal({ afnemer, onCancel, onConfirm, busy }: Props) {
  return (
    <div
      className="revoke-modal-overlay"
      role="dialog"
      aria-modal="true"
      aria-labelledby="revoke-modal-title"
      onClick={(e) => {
        if (e.target === e.currentTarget && !busy) onCancel()
      }}
    >
      <div className="revoke-modal-card">
        <h2 id="revoke-modal-title" className="revoke-modal-title">
          Toestemming intrekken?
        </h2>
        <p className="revoke-modal-body">
          U staat op het punt uw toestemming aan <strong>{afnemer}</strong> in te trekken.
        </p>
        <ul className="revoke-modal-list">
          <li>{afnemer} kan vanaf dat moment uw gegevens niet meer opvragen.</li>
          <li>Lopende processen kunnen hierdoor stagneren.</li>
          <li>U kunt later opnieuw toestemming geven via dezelfde dienst.</li>
        </ul>
        <div className="revoke-modal-actions">
          <button className="btn btn-secondary" onClick={onCancel} disabled={busy}>
            Annuleren
          </button>
          <button className="btn btn-deny" onClick={onConfirm} disabled={busy}>
            {busy ? 'Bezig…' : 'Ja, intrekken'}
          </button>
        </div>
      </div>
    </div>
  )
}
