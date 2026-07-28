import Hero from './components/Hero'
import FlowBand from './components/FlowBand'
import WhatIsGbo from './components/WhatIsGbo'
import Streams from './components/Streams'
import Pipeline from './components/Pipeline'
import TryItOut from './components/TryItOut'
import Infrastructure from './components/Infrastructure'
import SiteFooter from './components/SiteFooter'
import { useScrollProgress } from './hooks/useScrollProgress'
import { useReveals } from './hooks/useReveals'

export default function App() {
  const progressRef = useScrollProgress()
  useReveals()

  return (
    <>
      <div className="progress-track" aria-hidden="true">
        <div ref={progressRef} className="progress-bar" />
      </div>
      <Hero />
      <FlowBand />
      <main>
        <WhatIsGbo />
        <Streams />
        <Pipeline />
        <TryItOut />
        <Infrastructure />
      </main>
      <SiteFooter />
    </>
  )
}
