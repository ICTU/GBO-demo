import { Routes, Route } from 'react-router-dom'
import Start from './pages/Start'
import Return from './pages/Return'

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Start />} />
      <Route path="/return" element={<Return />} />
    </Routes>
  )
}
