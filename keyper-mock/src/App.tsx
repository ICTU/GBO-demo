import { Route, Routes } from 'react-router'
import { AanvraagDetail, NieuweAanvraag, StapGebouwen, StapInstallateur, StapReikwijdte } from './screens/installateur'
import { Geactiveerd, Goedkeuren, Mail, PortalTerug, Verzoek } from './screens/eigenaar'
import { Installatiegegevens, Onderhoudshistorie } from './screens/gegevens'

// Routes follow the demo flow: the installer's request (00–04), the owner's
// two consents with the ownership check at LVG on approval (05–11) and the
// installer's data behind a recurring check (12/14, 17).
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<NieuweAanvraag />} />
      <Route path="/aanvraag/installateur" element={<StapInstallateur />} />
      <Route path="/aanvraag/gebouwen" element={<StapGebouwen />} />
      <Route path="/aanvraag/reikwijdte" element={<StapReikwijdte />} />
      <Route path="/aanvraag" element={<AanvraagDetail />} />
      <Route path="/mail" element={<Mail />} />
      <Route path="/verzoek" element={<Verzoek />} />
      <Route path="/verzoek/terug" element={<PortalTerug />} />
      <Route path="/verzoek/goedkeuren" element={<Goedkeuren />} />
      <Route path="/verzoek/geactiveerd" element={<Geactiveerd />} />
      <Route path="/verzoek/afgewezen" element={<Geactiveerd rejected />} />
      <Route path="/installatiegegevens" element={<Installatiegegevens />} />
      <Route path="/onderhoudshistorie" element={<Onderhoudshistorie />} />
    </Routes>
  )
}
