import { Routes, Route, Navigate } from 'react-router-dom'
import DigiDAuth from './pages/DigiDAuth'
import PortaalConsent from './pages/PortaalConsent'
import PortaalBevestiging from './pages/PortaalBevestiging'
import MijnToestemmingenOverview from './pages/MijnToestemmingenOverview'
import MijnToestemmingenDetail from './pages/MijnToestemmingenDetail'
import MijnToestemmingenIngetrokken from './pages/MijnToestemmingenIngetrokken'

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/mijnoverheid/toestemmingen" replace />} />
      <Route path="/auth" element={<DigiDAuth />} />
      <Route path="/consent" element={<PortaalConsent />} />
      <Route path="/bevestiging" element={<PortaalBevestiging />} />
      <Route path="/mijnoverheid/toestemmingen" element={<MijnToestemmingenOverview />} />
      <Route
        path="/mijnoverheid/toestemmingen/:id"
        element={<MijnToestemmingenDetail />}
      />
      <Route
        path="/mijnoverheid/toestemmingen/:id/ingetrokken"
        element={<MijnToestemmingenIngetrokken />}
      />
    </Routes>
  )
}
