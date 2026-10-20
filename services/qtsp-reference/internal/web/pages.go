package web

import "html/template"

// style keeps the reference QTSP recognisable as a test client: neutral,
// without the look of a government service, readable on a phone.
const style = `body{font-family:system-ui,sans-serif;margin:0;line-height:1.5;color:#1f2328;background:#f6f8fa}
header{background:#24292f;color:#fff;padding:.8rem 1rem}header p{max-width:46rem;margin:0 auto;font-weight:600}
main{max-width:46rem;margin:1.5rem auto;padding:1.25rem;background:#fff;border:1px solid #d0d7de;border-radius:6px;box-sizing:border-box}
@media (max-width:48rem){main{margin:0;border:0;border-radius:0}}
.banner{background:#fff8c5;border:1px solid #d4a72c;padding:.6rem .8rem;border-radius:6px;margin-top:0}
code{overflow-wrap:anywhere}
fieldset{margin:1rem 0;border:1px solid #d0d7de;border-radius:6px}label{display:block;margin:.4rem 0}
textarea{width:100%;box-sizing:border-box;font-family:monospace;font-size:.85rem}
table{border-collapse:collapse;margin:1rem 0;width:100%}td,th{border:1px solid #d0d7de;padding:.4rem .6rem;text-align:left}
.ok{color:#1a7f37}.bad{color:#cf222e}
button{padding:.6rem 1.4rem;font-size:1rem;background:#1f883d;color:#fff;border:0;border-radius:6px;cursor:pointer}
button:disabled{background:#8c959f;cursor:not-allowed}
input{display:block;width:100%;box-sizing:border-box;padding:.4rem;font-size:1rem;margin-bottom:.6rem}
.who{background:#f6f8fa;border:1px solid #d0d7de;border-radius:6px;padding:.5rem .8rem}
.inline{display:inline}button.link{background:none;color:#0969da;padding:0 0 0 .5rem;text-decoration:underline}`

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
{{if .WalletQR}}<section>
<h2>Zet de verklaring in uw NL Wallet</h2>
<p>Deze verklaring is een test en geen gekwalificeerde elektronische attestatie van attributen.</p>
<p>Scan deze code met de NL Wallet op uw telefoon.</p>
<p><img src="{{.WalletQR}}" alt="QR-code om de testverklaring in de NL Wallet te zetten" width="280" height="280"></p>
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
