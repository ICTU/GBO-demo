import MijnOverheidLayout from '../components/MijnOverheidLayout'

// The one page for every refused reference (unknown, expired, used, another
// client_id or another citizen) and for a decision that failed without a
// return URL. It does not redirect and offers no way back: the portal no
// longer knows where the citizen came from, and a reference error must not
// send anyone anywhere.
export default function ToestemmingFout() {
  return (
    <MijnOverheidLayout activeNav="toestemmingen" breadcrumb={[{ label: 'Home' }]}>
      <div className="content-card">
        <h1 className="page-title">Dit verzoek kan niet worden afgehandeld</h1>
        <p>
          Het verzoek om toestemming is onbekend, verlopen of al gebruikt, of het hoort bij iemand
          anders. Ga terug naar de organisatie die u hierheen stuurde en begin daar opnieuw.
        </p>
      </div>
    </MijnOverheidLayout>
  )
}
