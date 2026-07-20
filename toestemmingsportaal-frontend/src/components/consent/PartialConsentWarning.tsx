type Props = { clientName: string }

export default function PartialConsentWarning({ clientName }: Props) {
  return (
    <div className="partial-warning" role="alert">
      <span className="icon" aria-hidden>⚠</span>
      <div>
        U deelt niet alle gevraagde gegevens. {clientName} kan uw aanvraag dan mogelijk niet volledig afronden.
      </div>
    </div>
  )
}
