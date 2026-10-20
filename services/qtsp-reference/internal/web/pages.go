package web

import "html/template"

// style follows the accessible ICTU/GBO web palette. The type, blue header and
// light-blue canvas connect this client to the GBO simulation environment,
// while its own service name and test banner remain prominent.
const style = `:root{--blue:#2e62e6;--blue-hover:#2651bd;--navy:#0e2b84;--purple:#880095;--cyan:#63dbff;--magenta:#ff33ff;--page:#ecf1fb;--info:#e6f8ff;--notice:#f8e6f9;--rule:#d7dff0;--muted:#4d5874;--danger:#a51c30;--success:#176b4d;font-family:"Century Gothic","Avenir Next",Avenir,ui-sans-serif,system-ui,sans-serif}
*{box-sizing:border-box}body{font-family:inherit;font-size:1rem;margin:0;line-height:1.55;color:#182247;background:var(--page)}
header{position:relative;overflow:hidden;background:var(--blue);padding:1.2rem 1.5rem;box-shadow:inset 0 -1px 0 rgba(255,255,255,.2)}header:after{content:"";position:absolute;right:-3rem;top:-6rem;width:15rem;height:15rem;border:1px solid rgba(255,255,255,.14);border-radius:50%}header p{position:relative;z-index:1;display:flex;align-items:center;gap:1rem;max-width:48rem;margin:0 auto;color:#fff;font-size:1rem;font-weight:400}header p:before{content:"GBO";font-size:1.1rem;font-weight:700;letter-spacing:.02em}
main{max-width:48rem;margin:2rem auto 3rem;padding:2rem;background:#fff;border:1px solid var(--rule);border-radius:6px;box-shadow:0 10px 30px rgba(14,43,132,.08)}
h1,h2{color:var(--navy);line-height:1.2;letter-spacing:-.015em}h1{font-size:2rem;margin:1.75rem 0 1.25rem}h2{font-size:1.45rem;margin:1.75rem 0 1rem}
p{margin:0 0 1rem}a{color:var(--navy);font-weight:700;text-decoration:underline;text-decoration-color:var(--blue);text-underline-offset:.2em}a:hover{color:var(--blue-hover)}
a:focus-visible,button:focus-visible,input:focus-visible,textarea:focus-visible{outline:3px solid var(--magenta);outline-offset:3px}
.banner{background:var(--notice);border:1px solid #e0b8e3;border-left:4px solid var(--purple);padding:.85rem 1rem;margin-top:0;color:#57105e}
code{overflow-wrap:anywhere;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:.9em}
fieldset{margin:1.25rem 0;border:1px solid var(--rule);border-radius:4px;padding:1rem 1.25rem}legend{color:var(--navy);font-weight:700;padding:0 .35rem}label{display:block;margin:.5rem 0}
textarea{width:100%;padding:.65rem;border:1px solid #7b86a4;border-radius:3px;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:.85rem;resize:vertical}
table{border-collapse:collapse;margin:1.25rem 0;width:100%}td,th{border:1px solid var(--rule);padding:.7rem .8rem;text-align:left}th{background:var(--page);color:var(--navy);font-weight:700}
.ok{background:var(--info);border-left:4px solid var(--success);color:#15583f;padding:.75rem 1rem}.bad{background:#fff0f2;border-left:4px solid var(--danger);color:#7d1525;padding:.75rem 1rem}
button{min-height:2.75rem;padding:.7rem 1.4rem;font-family:inherit;font-size:1rem;font-weight:700;background:var(--blue);color:#fff;border:0;border-radius:4px;cursor:pointer;box-shadow:0 2px 0 rgba(14,43,132,.18)}
button:hover{background:var(--blue-hover)}button:disabled{background:#9daedb;cursor:not-allowed;box-shadow:none}
input{display:block;width:100%;padding:.65rem;border:1px solid #7b86a4;border-radius:3px;font:inherit;margin-bottom:.75rem}input[type=radio],input[type=checkbox]{display:inline-block;width:auto;margin:0 .45rem 0 0;accent-color:var(--blue)}
.who{background:var(--page);border:1px solid var(--rule);border-radius:4px;padding:.7rem 1rem}.inline{display:inline}button.link{min-height:0;background:none;color:var(--navy);padding:0 0 0 .5rem;font-weight:700;text-decoration:underline;text-decoration-color:var(--blue);text-underline-offset:.18em;box-shadow:none}button.link:hover{background:none;color:var(--blue-hover)}
.wallet{border-top:3px solid var(--blue);margin-top:2rem;padding-top:.1rem}.qr{display:inline-flex;background:#fff;border:1px solid var(--rule);border-radius:4px;padding:.75rem}.qr img{display:block;max-width:100%;height:auto}
@media (max-width:50rem){header{padding-inline:1rem}main{margin:0;padding:1.5rem 1rem;border:0;border-radius:0;box-shadow:none}h1{font-size:1.75rem}.banner{margin-inline:0}.qr{display:flex;justify-content:center}}`

const banner = `<p class="banner"><strong>Referentie-QTSP (testomgeving).</strong> Een gekwalificeerde trust service provider laat attributen verifiëren of ophalen bij de bron (ETSI TS 119 478). Dit is de referentieclient van de demo, geen echte QTSP.</p>`

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Attestatie aanvragen</title><style>` + style + `</style></head><body>
<header><p>Referentie-QTSP · testclient</p></header>
<main>
` + banner + `
` + who + `
<h1>Attestatie aanvragen</h1>
<p>Kies de attributen die de QTSP bij de bron laat verifiëren (met de waarde die de QTSP van u heeft) of ophaalt. U wordt doorgestuurd naar de autorisatieserver van de bron om u te identificeren en toestemming te geven.</p>
<form method="post" action="/start">
<fieldset><legend>Wat</legend>
<label><input type="radio" name="operation" value="verify" checked> Verifiëren</label>
<label><input type="radio" name="operation" value="retrieve"> Ophalen</label>
</fieldset>
<fieldset><legend>Attributen</legend>
{{range .Attributes}}<label><input type="checkbox" name="use:{{.Identifier}}" value="1"{{if eq .Label "Familienaam"}} checked{{end}}> {{.Label}}</label>
<textarea name="value:{{.Identifier}}" rows="2">{{.Example}}</textarea>
{{end}}</fieldset>
<button type="submit">Aanvragen</button>
</form></main></body></html>
`))

var resultPage = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Resultaat</title><style>` + style + `</style></head><body>
<header><p>Referentie-QTSP · testclient</p></header>
<main>
` + banner + `
` + who + `
<h1>Resultaat</h1>
<p>Dataservice <code>{{.DataService}}</code>{{if .Provider}} van {{.Provider}}{{end}}{{if .Source}}, bron {{.Source}}{{end}}.</p>
{{if .Problem}}<p class="bad">De ASIP gaf status {{.ASIPStatus}}: {{.Problem}}</p>
{{else if not .Seal.Valid}}<p class="bad"><strong>Zegel ongeldig — resultaat niet geaccepteerd.</strong> {{.Seal.Error}}</p>
{{else}}<p class="ok">Zegel geldig, verzegeld door {{.Seal.Signer}}.</p>
<table><tr><th>Attribuut</th><th>{{if eq .Operation "verify"}}Uitkomst{{else}}Waarde{{end}}</th></tr>
{{range .Results}}<tr><td>{{.Label}}</td><td>{{.Display}}</td></tr>
{{end}}</table>{{end}}
{{if .WalletQR}}<section class="wallet">
<h2>Zet de verklaring in uw NL Wallet</h2>
<p>Deze verklaring is een test en geen gekwalificeerde elektronische attestatie van attributen.</p>
<p>Scan deze code met de NL Wallet op uw telefoon.</p>
<p class="qr"><img src="{{.WalletQR}}" alt="QR-code om de testverklaring in de NL Wallet te zetten" width="280" height="280"></p>
</section>{{end}}
<p><a href="/">Nieuwe aanvraag</a></p>
</main></body></html>
`))

var deniedPage = template.Must(template.New("denied").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Geweigerd</title><style>` + style + `</style></head><body>
<header><p>Referentie-QTSP · testclient</p></header>
<main>
` + banner + `
<h1>Geweigerd</h1>
<p>De aanvraag is niet toegestaan ({{.Error}}{{if .Description}}: {{.Description}}{{end}}). Er is niets bij de bron opgevraagd.</p>
<p><a href="/">Nieuwe aanvraag</a></p>
</main></body></html>
`))

var errPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Niet gelukt</title><style>` + style + `</style></head><body>
<header><p>Referentie-QTSP · testclient</p></header>
<main>
` + banner + `
<h1>Niet gelukt</h1>
<p>{{.Message}}</p>
<p><a href="/">Terug</a></p>
</main></body></html>
`))
