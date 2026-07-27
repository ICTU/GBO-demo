export default function Hero() {
  return (
    <section className="hero">
      <div className="hero-topbar">
        <div className="shell hero-topbar-row">
          <span className="wordmark">GBO</span>
          <nav className="hero-nav">
            <a href="#wat-is-gbo">Wat is GBO</a>
            <a href="#stromen">Stromen</a>
            <a href="#pipeline">Pipeline</a>
            <a href="#uitproberen" className="is-primary">
              Zelf uitproberen
            </a>
          </nav>
        </div>
      </div>
      <div className="hero-body">
        <div className="shell grid-12 grid-12--tight">
          <h1 className="span-7">
            Gemeenschappelijke Bronontsluiting
            <br />
            Demo-omgeving
          </h1>
          <div className="span-5 hero-aside">
            <p className="hero-lede">
              Gemeenschappelijke Bronontsluiting is de referentie-architectuur waarmee afnemers op
              een uniforme, getoetste manier gegevens uit bronsystemen bevragen. Deze demo draait
              live op de FSC-simulatieomgeving van datastelsel.nl.
            </p>
            <a href="#uitproberen" className="link-underline">
              Zelf uitproberen
            </a>
          </div>
        </div>
      </div>
    </section>
  )
}
