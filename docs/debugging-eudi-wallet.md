# Debugging the EUDI wallet flow

A runbook for when scanning a QR does not produce a credential. Written after
a migration to nl-wallet v0.5.0 in which the actual cause — our CA missing
from the wallet's trust list — took a full day to find, because the wallet
cannot report that particular failure at all.

Read the triage table first. Almost every wrong turn below came from
diagnosing before establishing which of the four states we were in.

## 1. Triage: what the session state tells you

The issuance-server persists every disclosure session. Its final state is the
single most discriminating signal available, and it is cheap to read:

```bash
docker compose exec -T postgres-eudi psql -U wallet -d issuance_server \
  -A -F'|' -c "select last_active_date_time, data::text from session_state order by last_active_date_time desc limit 5;"
```

| state | meaning | look at |
| --- | --- | --- |
| **no session at all** | the wallet never reached us | tunnel / reachability (§3) |
| **`WaitingForResponse`**, nothing posted | the wallet fetched the request and abandoned it *before it could report an error* | trust anchors (§4) — this is the silent one |
| **`FAILED`** with `error_description` | the wallet rejected the request and said why | read the message; it names the cause |
| **`CANCELLED`** | the wallet posted `access_denied` | user dismissed a screen, or no matching credential (§5) |

The asymmetry matters. A rejected certificate, a `client_id` mismatch, a bad
`response_uri` or an unsupported encryption algorithm all produce **FAILED
with a reason** — `report_error_back` in
`disclosure_session/client.rs` handles exactly that set. If you have a reason
to read, trust it.

`WaitingForResponse` with no follow-up means the opposite: the failure
happened somewhere that *structurally cannot* report. See §4.

## 2. Is the server even correct? Verify it in one step

Fetch the signed request the way the wallet does, over the public URL, and
decode it. This validates the whole server side at once — no phone needed:

```bash
JWT=$(curl -sS -X POST \
  "$EUDI_PUBLIC_URL/disclosure/<usecase>/request_uri?session_type=cross_device" \
  -H "Content-Type: application/x-www-form-urlencoded" --data "wallet_nonce=probe123")
python3 -c "
import base64,json,sys
h,p,_ = '''$JWT'''.strip().split('.')
d=lambda s: json.loads(base64.urlsafe_b64decode(s+'='*(-len(s)%4)))
hdr,pay = d(h),d(p)
print('alg',hdr['alg'],' x5c certs',len(hdr.get('x5c',[])))
for k in ('client_id','response_uri','response_mode','aud'): print(f'  {k:<14}',pay.get(k))
print('  dcql          ',json.dumps(pay.get('dcql_query'))[:200])
open('/tmp/leaf.der','wb').write(base64.b64decode(hdr['x5c'][0]))"
openssl x509 -inform DER -in /tmp/leaf.der -noout -subject -ext subjectAltName,extendedKeyUsage
```

What must hold under v0.5.0:

- `client_id` is `x509_san_dns:<host>` — the scheme prefix is **mandatory**,
  there is no fallback
- that `<host>` is a DNS SAN of the reader certificate
- the `response_uri` **FQDN equals** `client_id`'s host
- EKU is `1.0.18013.5.1.6` (ReaderAuth); `aud` is `https://self-issued.me/v2`

All three of the first are derived from `EUDI_PUBLIC_URL`, so they cannot
drift — the server derives its `client_id` in `verifier.rs`
(`client_id_from_public_url`) and the frontends derive theirs from the same
variable.

## 3. Reachability

```bash
curl -sS -o /dev/null -w "%{http_code}\n" "$EUDI_PUBLIC_URL"
```

`530` is Cloudflare for "tunnel not connected". `404` is healthy — the
issuance-server serves no `/`.

The tunnel is **not** started by `make demo-full`; it lives in a separate
compose file. Use:

```bash
COMPOSE_FILE=docker-compose.yml:docker-compose.cloudflare-tunnel.yml make demo-full
```

Do not put `COMPOSE_FILE` in `.env` — without a profile the overlay fails to
parse (`eudi-tunnel depends on undefined service eudi-issuance-server`),
breaking `make demo-minimal`.

## 4. The silent failure: trust anchors

If the session sits in `WaitingForResponse` and the app shows a generic error
with **no organisation name**, the wallet could not verify our reader
certificate.

Why it is silent, from `disclosure_session/client.rs`:

```rust
let (vp_auth_request, certificate) = VpAuthorizationRequest::try_new(&jws, trust_anchors)?;
let response_uri = vp_auth_request.response_uri.clone();   // ← extracted only AFTER
```

`try_new` verifies the x5c chain against the wallet's trust anchors. The `?`
propagates immediately — and `response_uri` does not exist yet, so the wallet
has nowhere to send an error. The verifier sees a session hang forever with no
diagnostic. `RpCertificate` *is* in `report_error_back`, but it is only
reachable from `process_auth_request`, which runs later. **Do not conclude
from that list that a certificate problem would have been reported.**

The absence of the organisation name is corroborating: the app picks
`…ErrorDescriptionWithOrganizationName` only when it has one, and it has one
only if the certificate parsed and verified.

### Read the wallet's trust list

It is public. No key needed — the JWT payload is plain base64:

```bash
curl -sS --max-time 15 https://static.preproductie.wallet.edi.bzk.nl/config/v1/wallet-config | python3 -c "
import base64, json, sys
from cryptography import x509
p = sys.stdin.read().strip().split('.')[1]
d = json.loads(base64.urlsafe_b64decode(p + '='*(-len(p)%4)))
print(f\"wallet config v{d['version']} ({d['environment']})\")
for label, anchors in (('READER / RP (may request)', d['disclosure']['rp_trust_anchors']),
                       ('ISSUER (may issue)',        d['issuer_trust_anchors'])):
    print(f'\n{label}:')
    for b in anchors:
        c = x509.load_der_x509_certificate(base64.b64decode(b))
        print(f\"  {c.subject.rfc4514_string().split(',')[0]:<52} exp {c.not_valid_after_utc.date()}\")
"
```

URL pattern: `https://static.<environment>.wallet.edi.bzk.nl/config/v1/wallet-config`
(from `scripts/generate-wallet-config.sh` in the nl-wallet sources).

Our CA must appear in **`disclosure.rp_trust_anchors`**. If it does not, no
local change can fix it — BZK's operations team maintains that list. Their own
documentation (`community/create-a-ca.html`) says: *"You can e-mail the public
key to the e-mail address of the operations team. They will configure this key
in the issuer and reader trust anchors of the NL Wallet."*

Two things to state when asking:

- it must be a **version bump**; `if new_config.version <= current` rejects a
  same-version republish, so editing in place reaches nobody
- it must be signed with the **same key**, which is compiled into the app

The app polls hourly and persists the result to `storage_path/configuration.jwt`,
so a published fix reaches devices without an app release. Re-run the command
above to watch the `version` change.

## 5. `CANCELLED`: the wallet declined

`CANCELLED` means the wallet posted `access_denied` — reached only via
`terminate()`, i.e. the app cancelled. Two realistic causes:

- the user dismissed an error screen
- **no matching credential.** On missing attributes the app deliberately does
  not respond ("Store the session so that it will only be terminated on user
  interaction. This prevents gleaning of missing attributes by a verifier"),
  so the session waits until the user taps away, then goes `CANCELLED`

A third, easy to miss: **every claim the credential carries in plaintext must
be explicitly requested.** `verify_non_selectively_disclosable_claims` rejects
a request that omits any non-selectively-disclosable claim — and it runs
*before* the missing-attributes check, on already-matched credentials.

Note `fetch_candidate_attestations` only selects credentials containing **all**
requested claims, so over-requesting to test something makes a credential stop
matching entirely and produces a *different* silent failure. Bisect carefully.

## 6. Tracing an app error to its cause

The method that finally worked, when the server side looks correct:

1. Grep the exact Dutch string in `wallet_app/lib/l10n/intl_nl.arb` → gives a
   key such as `issuanceRelyingPartyErrorDescription`
2. Grep that key in `*.dart` → the state class (`IssuanceRelyingPartyError`)
3. Follow to `core_error_mapper.dart` → the `FlutterApiErrorType`
4. Grep that variant in `wallet_core/flutter_api/src/errors.rs` → the Rust
   error variants that map to it
5. Grep those variants in `wallet_core/wallet/src/` → the code that raises them

The sources are in `vendor/nl-wallet`, so this is all local:
`git -C vendor/nl-wallet grep -rn "<needle>" v0.5.0 -- '*.rs'`.

Also tap **"Bekijk details"** on the error screen. It gives the app version,
build commit and **config version** — enough to confirm the app matches our
pinned submodule commit and which trust list it is using.

## 7. Traps that cost real time

**Stale issuance-server image.** `make eudi-images` used to skip whenever any
image existed, silently running the previous pin against the new config.
It now stamps the nl-wallet revision as a label and compares. If the build
says a version you did not expect, check `NLWALLET_PATH` in `.env` — an
override there wins over the submodule via `?=`.

**Submodule not updated on branch switch.** `git submodule status` showing a
leading `+` means the checkout differs from what the branch pins. Switching
branches does not move submodules.

**`restart` versus `up -d`.** Certificates arrive through a *bind-mounted*
file, so compose cannot see the change: `up -d` alone will not restart the
issuance-server. Frontend env vars are the opposite: `restart` does not re-read
`.env`. After a change touching both, you need all three:

```bash
make eudi-config && docker compose --profile eudi up -d && \
  docker compose --profile eudi restart eudi-issuance-server
```

**Database migration history.** Upgrading across an nl-wallet version that
renames a migration fails with *"migration has been applied but its file is
missing"* — v0.5.0 renamed `m20250925_000003_create_status_lists` to
`…_create_status_list` (content identical). Either rename the row in
`seaql_migrations` or wipe the volume with `down -v`.

**Hand-editing `.env`.** Appending leaves the previous assignment of a slot
above the new one; last-wins hides it. Use `scripts/update-env.py`, which
replaces in place, removes later duplicates and backs up first.

**Single-file bind mounts break on `git checkout`.** The frontends mount
individual files (`vite.config.ts`, `index.html`, `tsconfig.json`), and a bind
mount of a *file* binds the inode. `git checkout` unlinks and recreates rather
than editing in place, so after a branch switch the container sees
`No such file or directory` while `docker inspect` still lists the mount.
Vite then restarts without any proxy configuration and every `/api/dev/*`
request falls through to the SPA fallback — the browser reports
*"Unexpected token '<', "<!doctype "... is not valid JSON"*, which reads like
a backend failure but is a stale mount. Confirm and fix:

```bash
docker compose exec -T developer-portal ls -l /app/vite.config.ts   # missing?
docker compose --profile eudi up -d --force-recreate developer-portal
```

Plain `up -d` will not do it: nothing in the service definition changed, so
compose sees no reason to recreate. The same applies to any frontend after a
branch switch.

**`docker compose logs --since` is unreliable here.** Filter on the container
timestamp yourself:

```bash
docker compose logs --timestamps --no-log-prefix <svc> | awk '$1 >= "2026-08-03T09:00:00"'
```

**Services log in different timezones.** Compare on the Docker timestamp, not
the application's own.

**`make eudi-config` overwrites the rendered config.** When testing a change
to `issuance_server.toml` directly, do not run it until you are done — and it
is also the easiest way to revert.

## 8. What changed in v0.5.0

- `client_id` must carry the `x509_san_dns:` prefix; the loose
  `client_id_scheme` field is gone. Server and app move in **lockstep**
- the `EUDI_PUBLIC_URL` host became part of the crypto: reader certificates
  must list it as a DNS SAN, and `response_uri`'s FQDN must equal it
- PID and address merged into one `urn:eudi:pid:nl:1`; the address is now the
  sub-claim `urn:eudi:pid:nl:1.address`
- `EUDI_CLIENT_ID` and `EUDI_READER_ORIGIN_URL` were removed — both are now
  derived from `EUDI_PUBLIC_URL`, because the server admits no other value

## 9. Known-good reference values

| | |
| --- | --- |
| trust list | `https://static.preproductie.wallet.edi.bzk.nl/config/v1/wallet-config` |
| universal link base | `https://app.preproductie.wallet.edi.bzk.nl/deeplink/disclosure_based_issuance` |
| PID vct | `urn:eudi:pid:nl:1`, format `dc+sd-jwt` |
| our CAs | `ca.gbo-reader` (readers), `ca.gbo-issuer` (issuers + status list) |
| reader EKU | `1.0.18013.5.1.6` |
| issuer EKU | `1.0.18013.5.1.2` |
| status-list EKU | `1.3.6.1.5.5.7.3.127` |

Neither CA carries a name-constraints extension, so leaf hostnames are free —
only the CA has to stay fixed, because that is what the wallet trusts.
**Never mint a new CA to solve a hostname problem**: a leaf under an unknown
root is rejected by every wallet and cannot be recovered locally.
