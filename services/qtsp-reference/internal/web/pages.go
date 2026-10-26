package web

import "html/template"

// style is the QTSP's own look: a private party in green and teal, after the
// mortgage lender of the demo, so that a viewer tells it apart from the
// authorization server and the government portals at a glance. One style
// sheet, no script, no external fonts.
const style = `:root{--hd:#133D38;--pri:#0F8A7A;--pri-d:#0B6B5F;--pri-dd:#095449;--h:#0A2845;--tx:#2A3E55;--mu:#6B7280;--ln:#E5E7EB;--ln2:#EEF0F3;--bg:#F7F8F7;--ok:#0F8A6A;--ok-t:#0A5C47;--ok-bg:#E6F4EF;--wa-bg:#FEF5E7;--wa-ln:#F2D49B;--wa-t:#8A5A12;--no-bg:#FCECEC;--no-ln:#F1C2C2;--no-t:#9B1C1C;--un-bg:#F1F2F4;--un-t:#4B5563;--demo-bg:#FFF4D9;--demo-ln:#E7C778;--demo-t:#7E4A00}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;background:var(--bg);color:var(--tx);font:15px/1.55 ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;-webkit-font-smoothing:antialiased}
a{color:var(--pri-d);text-decoration:underline;text-underline-offset:3px}a:hover{color:var(--pri-dd)}
:focus-visible{outline:3px solid #F2B700;outline-offset:2px}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.9em;overflow-wrap:anywhere}
h1{font-size:26px;line-height:1.25;color:var(--h);margin:0 0 10px;font-weight:700;letter-spacing:-.01em}
h2{font-size:17px;line-height:1.3;color:var(--h);margin:0 0 10px;font-weight:700}
p{margin:0 0 14px}.lead{font-size:15.5px}.sec{color:var(--mu);font-size:13.5px}
.qp-hd{background:var(--hd);color:#fff}
.qp-hd-in{max-width:880px;margin:0 auto;padding:14px 20px;display:flex;align-items:center;gap:16px;flex-wrap:wrap}
.qp-brand{display:flex;align-items:center;gap:11px;color:#fff;text-decoration:none;min-width:0}.qp-brand:hover{color:#fff}
.qp-brand svg{flex:0 0 30px}
.qp-brand b{display:block;font-size:16px;font-weight:700;line-height:1.2}
.qp-brand small{display:block;font-size:12.5px;color:#BFD6D1;line-height:1.3}
.qp-user{margin-left:auto;font-size:13.5px;color:#DCEAE6;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.qp-user strong{color:#fff}
.qp-user form{display:inline;margin:0}
.qp-user button{background:none;border:0;padding:0;font:inherit;color:#fff;text-decoration:underline;text-underline-offset:3px;cursor:pointer}
.qp-demo{background:var(--demo-bg);border-bottom:1px solid var(--demo-ln);color:var(--demo-t)}
.qp-demo-in{max-width:880px;margin:0 auto;padding:9px 20px;display:flex;gap:10px;align-items:flex-start;font-size:13.5px;line-height:1.45}
.qp-tag{display:inline-block;border:1px solid var(--demo-ln);background:#fff;border-radius:4px;padding:1px 7px;font-size:12px;font-weight:700;white-space:nowrap}
.qp-main{max-width:880px;margin:0 auto;padding:28px 20px 40px}
.qp-card{background:#fff;border:1px solid var(--ln);border-radius:10px;padding:28px 30px}
.qp-card+.qp-card{margin-top:16px}
.qp-narrow{max-width:600px;margin-left:auto;margin-right:auto}
.qp-ft{max-width:880px;margin:0 auto;padding:0 20px 28px;color:var(--mu);font-size:12.5px}
.qp-btn{display:inline-flex;align-items:center;justify-content:center;gap:9px;min-height:46px;padding:11px 20px;border-radius:8px;border:1.5px solid var(--pri-d);background:var(--pri-d);color:#fff;font:inherit;font-weight:700;font-size:15px;text-decoration:none;cursor:pointer;line-height:1.2}
.qp-btn:hover{background:var(--pri-dd);border-color:var(--pri-dd);color:#fff}
.qp-btn.sec{background:#fff;color:var(--pri-d)}.qp-btn.sec:hover{background:#EEF7F5;color:var(--pri-d)}
.qp-btn.block{width:100%}
.qp-btn:disabled{background:#E5E7EB;border-color:#D1D5DB;color:var(--mu);cursor:not-allowed}
.qp-actions{display:flex;flex-wrap:wrap;gap:12px;align-items:center;margin-top:20px}
.qp-note{display:flex;gap:10px;border-radius:8px;padding:11px 14px;font-size:14px;line-height:1.5;margin:0 0 16px}
.qp-note.warn{background:var(--wa-bg);border:1px solid var(--wa-ln);color:var(--wa-t)}
.qp-or{display:flex;align-items:center;gap:12px;color:var(--mu);font-size:13px;margin:24px 0}
.qp-or::before,.qp-or::after{content:"";flex:1;height:1px;background:var(--ln)}
fieldset{border:1px solid var(--ln);border-radius:10px;padding:18px 20px 20px;margin:0;min-width:0}
legend{padding:0 8px;font-weight:700;color:var(--h);font-size:15px}
.qp-fld{margin-bottom:14px}
.qp-fld label{display:block;font-weight:600;color:var(--h);font-size:14px;margin-bottom:5px}
.qp-fld .hint{font-weight:400;color:var(--mu)}
input[type=text]{width:100%;min-height:46px;border:1.5px solid #C9CED6;border-radius:7px;padding:10px 12px;font:inherit;font-size:16px;color:var(--h);background:#fff}
input[type=text]:focus{border-color:var(--pri-d)}
.qp-grid2{display:grid;grid-template-columns:1fr 1fr;gap:0 16px;align-items:end}
.qp-prods{border:0;margin:0 0 20px;padding:0;display:flex;flex-direction:column;gap:12px;min-width:0}
.qp-prods>legend{font-size:17px;font-weight:700;color:var(--h);padding:0 0 10px}
.qp-prod{border:1.5px solid var(--ln);border-radius:10px;background:#fff}
.qp-prod-h{display:flex;gap:13px;align-items:flex-start;padding:16px 18px;cursor:pointer}
.qp-prod-h>div{min-width:0}
.qp-prod-h input{accent-color:var(--pri-d);width:19px;height:19px;margin:2px 0 0;flex:0 0 19px}
.qp-prod-h b{display:block;color:var(--h);font-size:16px}
.qp-prod-h span{display:block;font-size:14px;color:var(--tx)}
.qp-prod-f{border-top:1px solid var(--ln2);padding:16px 18px 4px}
.qp-prod.off{background:#FAFAFA;border-style:dashed}
.qp-prod.off .qp-prod-h{cursor:not-allowed}
.qp-prod.off b,.qp-prod.off span{color:var(--mu)}
.qp-soon{margin-left:auto;flex:0 0 auto;font-size:12px;font-weight:700;color:var(--un-t);background:var(--un-bg);border:1px solid #D6D9DE;border-radius:999px;padding:2px 10px;font-style:normal}
@supports selector(:has(*)){.qp-prod:has(.qp-prod-h input:checked){border-color:var(--pri-d);box-shadow:0 0 0 1px var(--pri-d)}.qp-prod:not(:has(.qp-prod-h input:checked)) .qp-prod-f{display:none}}
.qp-g-adr{display:grid;grid-template-columns:2fr 1fr;gap:0 14px;align-items:end}.qp-g-adr .w2{grid-column:1/-1}
.qp-fld.err input{border-color:#C53030;border-width:2px}
.qp-fmsg{display:block;color:var(--no-t);font-size:13.5px;font-weight:600;margin-top:5px}
.qp-errsum{border:1.5px solid var(--no-ln);background:var(--no-bg);color:var(--no-t);border-radius:9px;padding:13px 16px;margin:0 0 18px;font-size:14px}
.qp-errsum b{display:block;margin-bottom:3px}.qp-errsum ul{margin:0;padding-left:18px}.qp-errsum a{color:var(--no-t)}
.qp-tbl{width:100%;border-collapse:collapse;font-size:14.5px}
.qp-tbl th{text-align:left;font-size:12.5px;font-weight:700;color:var(--mu);padding:0 12px 9px;border-bottom:1.5px solid var(--ln)}
.qp-tbl td{padding:12px;border-bottom:1px solid var(--ln2);vertical-align:middle}
.qp-tbl tr:last-child td{border-bottom:0}
.qp-tbl .attr{font-weight:600;color:var(--h)}
.qp-badge{display:inline-flex;align-items:center;gap:6px;border-radius:999px;padding:4px 11px 4px 8px;font-size:13px;font-weight:700;white-space:nowrap;border:1px solid}
.qp-badge i{font-style:normal;display:inline-grid;place-items:center;width:18px;height:18px;border-radius:50%;font-size:11px;color:#fff}
.qp-badge.match{background:var(--ok-bg);border-color:#B9E0D3;color:var(--ok-t)}.qp-badge.match i{background:var(--ok)}
.qp-badge.var{background:var(--wa-bg);border-color:var(--wa-ln);color:var(--wa-t)}.qp-badge.var i{background:#B7791F}
.qp-badge.no{background:var(--no-bg);border-color:var(--no-ln);color:var(--no-t)}.qp-badge.no i{background:#C53030}
.qp-badge.unk{background:var(--un-bg);border-color:#D6D9DE;color:var(--un-t)}.qp-badge.unk i{background:#6B7280}
.qp-seal{display:flex;gap:14px;align-items:flex-start;border:1px solid #B9E0D3;background:var(--ok-bg);border-radius:9px;padding:16px 18px;color:var(--ok-t)}
.qp-seal.bad{border-color:var(--no-ln);background:var(--no-bg);color:var(--no-t)}
.qp-seal svg{flex:0 0 34px}
.qp-qrwrap{display:flex;gap:24px;align-items:center}
.qp-qr{flex:0 0 220px;width:220px;height:220px;padding:12px;background:#fff;border:1px solid var(--ln);border-radius:10px}
.qp-qr img{display:block;width:100%;height:100%;image-rendering:pixelated}
.qp-steps{margin:0 0 10px;padding-left:20px}.qp-steps li{margin-bottom:6px}
.qp-live{display:inline-flex;align-items:center;gap:8px;color:var(--mu);font-size:13px}
.qp-live::before{content:"";width:8px;height:8px;border-radius:50%;background:var(--pri)}
.qp-err{display:flex;gap:16px;align-items:flex-start}
.qp-err .ic{flex:0 0 44px;width:44px;height:44px;border-radius:50%;display:grid;place-items:center;font-weight:800;font-size:20px}
.qp-err .ic.no{background:var(--no-bg);color:var(--no-t);border:1px solid var(--no-ln)}
.qp-err .ic.wa{background:var(--wa-bg);color:var(--wa-t);border:1px solid var(--wa-ln)}
@media (max-width:620px){.qp-hd-in{padding:12px 16px}.qp-user{margin-left:0;width:100%}.qp-demo-in{padding:9px 16px}.qp-main{padding:18px 12px 28px}.qp-card{padding:20px 16px;border-radius:8px}h1{font-size:22px}.qp-grid2,.qp-g-adr{grid-template-columns:1fr}.qp-prod-h,.qp-prod-f{padding-left:14px;padding-right:14px}.qp-btn{width:100%}.qp-tbl thead{display:none}.qp-tbl tr{display:grid;grid-template-columns:1fr auto;gap:4px 10px;padding:12px 0;border-bottom:1px solid var(--ln2)}.qp-tbl tr:last-child{border-bottom:0}.qp-tbl td{padding:0;border:0}.qp-tbl td.val{grid-column:1/-1;order:3}.qp-qrwrap{flex-direction:column;align-items:stretch}.qp-qr{width:100%;max-width:260px;height:auto;aspect-ratio:1;margin:0 auto}.qp-ft{padding:0 16px 24px}}`

// head opens a page; the title is filled in per page.
const head = `<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">`

// top is the header, with the logged-in citizen when there is one, and the
// test banner.
const top = `<header class="qp-hd"><div class="qp-hd-in">
<a class="qp-brand" href="/"><svg width="30" height="34" viewBox="0 0 30 34" aria-hidden="true"><path d="M15 1.5 27.5 6v11c0 7.6-5.3 12.6-12.5 15.5C7.8 29.6 2.5 24.6 2.5 17V6Z" fill="#0F8A7A" stroke="#fff" stroke-width="1.6"/><path d="m9.5 17 4 4 7-8" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"/></svg><span><b>GBO demo test QTSP</b><small>Vertrouwensdienst — testomgeving</small></span></a>
` + who + `
</div></header>
<div class="qp-demo"><div class="qp-demo-in"><span class="qp-tag">TEST</span><div><strong>Test, niet gekwalificeerd.</strong> Dit is de referentieclient van de demo, geen echte vertrouwensdienst (ETSI TS 119 478). Verklaringen hebben geen rechtsgeldigheid.</div></div></div>`

const foot = `<footer class="qp-ft">GBO demo test QTSP · fictieve vertrouwensdienst · test, niet gekwalificeerd</footer>
</body></html>
`

var formPage = template.Must(template.New("form").Parse(head + `
<title>Attestatie aanvragen</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main"><form class="qp-card" method="post" action="/start" novalidate>
<h1>Attestatie aanvragen</h1>
<p class="lead">Kies welke gegevens u wilt laten controleren bij de bron. Daarna kunt u de verklaring in uw NL Wallet zetten.</p>
{{with .Form.Summary}}<div class="qp-errsum" role="alert"><b>Controleer het formulier</b><ul>{{range .}}<li>{{if .Target}}<a href="#{{.Target}}">{{.Message}}</a>{{else}}{{.Message}}{{end}}</li>{{end}}</ul></div>{{end}}
<fieldset class="qp-prods">
<legend>Wat wilt u laten verifiëren?</legend>
{{$form := .Form}}{{range .Products}}{{$p := .}}{{if .Available}}<div class="qp-prod">
<label class="qp-prod-h"><input type="radio" name="product" value="{{.ID}}"{{if eq $form.Selected .ID}} checked{{end}}><div><b>{{.Label}}</b><span>{{.Description}}</span></div></label>
<div class="qp-prod-f"><div class="qp-g-adr">
{{range .Fields}}{{$id := printf "f-%s-%s" $p.ID .Name}}{{$err := index $form.Errors $id}}<div class="qp-fld{{if .Wide}} w2{{end}}{{if $err}} err{{end}}"><label for="{{$id}}">{{.Label}}{{if .Hint}} <span class="hint">({{.Hint}})</span>{{end}}</label><input type="text" id="{{$id}}" name="{{$id}}" value="{{index $form.Values $id}}" autocomplete="off"{{if $err}} aria-invalid="true" aria-describedby="m-{{$id}}"{{end}}>{{if $err}}<span class="qp-fmsg" id="m-{{$id}}">{{$err}}</span>{{end}}</div>
{{end}}</div></div>
</div>
{{else}}<div class="qp-prod off">
<label class="qp-prod-h"><input type="radio" name="product" value="{{.ID}}" disabled><div><b>{{.Label}}</b><span>{{.Description}}</span></div><em class="qp-soon">Binnenkort</em></label>
</div>
{{end}}{{end}}</fieldset>
<div class="qp-actions"><button class="qp-btn" type="submit">Aanvragen</button><span class="sec">U wordt doorgestuurd naar de autorisatieserver van de bron om toestemming te geven.</span></div>
</form></main>
` + foot))

var resultPage = template.Must(template.New("result").Funcs(template.FuncMap{"badge": badge, "shown": shown}).Parse(head + `
<title>Resultaat</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main">
<section class="qp-card">
<h1>Resultaat</h1>
<p class="lead">{{if .Source}}{{.Source}}{{else}}De bron{{end}} heeft {{if eq .Operation "verify"}}de opgegeven gegevens gecontroleerd{{else}}de gevraagde gegevens gegeven{{end}}.</p>
<p class="sec">Dataservice <code>{{.DataService}}</code>{{if .Provider}} van {{.Provider}}{{end}}.</p>
{{if .Problem}}<div class="qp-errsum" role="alert"><b>De ASIP gaf status {{.ASIPStatus}}</b>{{.Problem}}</div>
{{else if not .Seal.Valid}}<div class="qp-errsum" role="alert"><b>Zegel ongeldig — resultaat niet geaccepteerd.</b>{{.Seal.Error}}</div>
{{else}}<table class="qp-tbl">
<thead><tr><th>Gegeven</th>{{if eq .Operation "verify"}}<th>Opgegeven waarde</th><th>Uitkomst</th>{{else}}<th>Waarde</th>{{end}}</tr></thead>
<tbody>{{$verify := eq .Operation "verify"}}{{range .Results}}<tr><td class="attr">{{.Label}}</td>{{if $verify}}<td class="val">{{shown .Submitted}}</td><td>{{with badge .Result}}<span class="qp-badge {{.Class}}"><i aria-hidden="true">{{.Icon}}</i>{{.Text}}</span>{{end}}</td>{{else}}<td class="val">{{shown .Value}}</td>{{end}}</tr>
{{end}}</tbody></table>{{end}}
</section>
{{if and (not .Problem) .Seal}}<section class="qp-card">
<h2>Zegelcontrole</h2>
{{if .Seal.Valid}}<div class="qp-seal"><svg width="34" height="34" viewBox="0 0 34 34" aria-hidden="true"><circle cx="17" cy="17" r="15" fill="#0F8A6A"/><path d="m10.5 17.5 4.2 4.2 8.8-9" fill="none" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/></svg><div><strong>Zegel geldig</strong>, verzegeld door {{.Seal.Signer}}. Het antwoord komt van de bron en is niet gewijzigd.</div></div>
{{else}}<div class="qp-seal bad"><div><strong>Zegel ongeldig</strong>: het resultaat is niet geaccepteerd. {{.Seal.Error}}</div></div>{{end}}
</section>{{end}}
{{if .WalletQR}}<section class="qp-card">
<h2>Zet de verklaring in uw NL Wallet</h2>
<div class="qp-qrwrap">
<div class="qp-qr"><img src="{{.WalletQR}}" alt="QR-code om de testverklaring in de NL Wallet te zetten" width="280" height="280"></div>
<div><p>Scan deze code met de NL Wallet op uw telefoon.</p>
<div class="qp-note warn"><span aria-hidden="true">!</span><div>Deze verklaring is een test en geen gekwalificeerde elektronische attestatie van attributen.</div></div></div>
</div>
</section>{{end}}
<div class="qp-actions"><a class="qp-btn sec" href="/">Nieuwe aanvraag</a></div>
</main>
` + foot))

var deniedPage = template.Must(template.New("denied").Parse(head + `
<title>Geweigerd</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main"><div class="qp-card qp-narrow"><div class="qp-err"><div class="ic no" aria-hidden="true">✕</div><div>
<h1>Geweigerd</h1>
<p>De aanvraag is niet toegestaan. Er is niets bij de bron opgevraagd en er is geen verklaring gemaakt.</p>
<p class="sec">Foutcode: <span class="mono">{{.Error}}</span>{{if .Description}} · {{.Description}}{{end}}</p>
<div class="qp-actions"><a class="qp-btn" href="/">Nieuwe aanvraag</a></div>
</div></div></div></main>
` + foot))

var errPage = template.Must(template.New("error").Parse(head + `
<title>Niet gelukt</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main"><div class="qp-card qp-narrow"><div class="qp-err"><div class="ic wa" aria-hidden="true">!</div><div>
<h1>Niet gelukt</h1>
<p>{{.Message}}</p>
<div class="qp-actions"><a class="qp-btn" href="/">Terug</a></div>
</div></div></div></main>
` + foot))
