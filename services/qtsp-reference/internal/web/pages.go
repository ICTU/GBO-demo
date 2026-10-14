package web

import "html/template"

const style = `body{font-family:system-ui,sans-serif;max-width:46rem;margin:2rem auto;padding:0 1rem;line-height:1.5}
.banner{background:#fff4ce;border:1px solid #e0c060;padding:.6rem .8rem;border-radius:4px}
fieldset{margin:1rem 0}label{display:block;margin:.4rem 0}textarea{width:100%;font-family:monospace;font-size:.85rem}
table{border-collapse:collapse;margin:1rem 0}td,th{border:1px solid #ccc;padding:.3rem .6rem;text-align:left}
.ok{color:#1a7f37}.bad{color:#cf222e}button{padding:.5rem 1.2rem;font-size:1rem}`

const banner = `<p class="banner"><strong>Referentie-QTSP (testomgeving).</strong> Een gekwalificeerde trust service provider laat attributen verifiëren of ophalen bij de bron (ETSI TS 119 478). Dit is de referentieclient van de demo, geen echte QTSP.</p>`

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Attestatie aanvragen</title><style>` + style + `</style></head><body>
` + banner + `
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
</form></body></html>
`))

var resultPage = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><title>Resultaat</title><style>` + style + `</style></head><body>
` + banner + `
<h1>Resultaat</h1>
<p>Dataservice <code>{{.DataService}}</code>{{if .Provider}} van {{.Provider}}{{end}}{{if .Source}}, bron {{.Source}}{{end}}.</p>
{{if .Problem}}<p class="bad">De ASIP gaf status {{.ASIPStatus}}: {{.Problem}}</p>
{{else if not .Seal.Valid}}<p class="bad"><strong>Zegel ongeldig — resultaat niet geaccepteerd.</strong> {{.Seal.Error}}</p>
{{else}}<p class="ok">Zegel geldig, verzegeld door {{.Seal.Signer}}.</p>
<table><tr><th>Attribuut</th><th>{{if eq .Operation "verify"}}Uitkomst{{else}}Waarde{{end}}</th></tr>
{{range .Results}}<tr><td>{{.Label}}</td><td>{{.Display}}</td></tr>
{{end}}</table>{{end}}
<p><a href="/">Nieuwe aanvraag</a></p>
</body></html>
`))

var deniedPage = template.Must(template.New("denied").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><title>Geweigerd</title><style>` + style + `</style></head><body>
` + banner + `
<h1>Geweigerd</h1>
<p>De aanvraag is niet toegestaan ({{.Error}}{{if .Description}}: {{.Description}}{{end}}). Er is niets bij de bron opgevraagd.</p>
<p><a href="/">Nieuwe aanvraag</a></p>
</body></html>
`))

var errPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><title>Niet gelukt</title><style>` + style + `</style></head><body>
` + banner + `
<h1>Niet gelukt</h1>
<p>{{.Message}}</p>
<p><a href="/">Terug</a></p>
</body></html>
`))
