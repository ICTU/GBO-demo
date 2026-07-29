#!/usr/bin/env python3
"""Mint the EUDI leaf certificates for one deployment.

Every deployment mints its own leaves. What it does NOT mint is the two
CAs — `ca.gbo-issuer` and `ca.gbo-reader` are what the preprod wallet has
been configured to trust, so they are fixed and shared. Leaf hostnames are
free: neither CA carries a name-constraints extension, so a leaf for
`eudi-is.simulatie.datastelsel.nl` is as valid under them as one for
`.gbo.b15.io`.

Five key/certificate pairs, in two groups:

  READERS — `EUDI_READER_*` (Belastingdienst) and `EUDI_BRP_READER_*` (RvIG),
  under ca.gbo-reader. Both carry the host of EUDI_PUBLIC_URL as subject CN
  and DNS SAN. That is not cosmetic: since nl-wallet v0.5.0 the
  issuance-server derives its OpenID4VP client_id from its own public_url as
  `x509_san_dns:<host>` and refuses to build a disclosure use case whose
  reader certificate does not list that host (verifier.rs,
  client_id_from_public_url). They differ only in the embedded reader-auth
  JSON, which is the purpose statement and organisation the wallet renders
  when it asks for the BSN.

  ISSUERS — `EUDI_ISSUER_*` (Belastingdienst), `EUDI_BRP_ISSUER_*` (RvIG) and
  `EUDI_STATUS_*` (token status list), under ca.gbo-issuer. These are NOT
  bound to EUDI_PUBLIC_URL. They are bound to each other: the issuance-server
  refuses to start unless, per attestation type, the attestation certificate
  and the status-list certificate share a subject. Since both attestation
  types use one status certificate, all three must carry the same subject —
  which this script guarantees by minting them from a single --issuer-host.
  That host also becomes the credential's `iss` (a DNS SAN is read as
  `https://<san>`), so it is an identity, not an address: it need not
  resolve, but it is what a wallet shows as the issuer.

Change EUDI_PUBLIC_URL and you must re-mint the readers. The issuers only
need re-minting when you want their identity to match a new environment —
note that a fresh EUDI_STATUS_* invalidates the status lists of already
issued credentials, which for a demo means re-issuing them.

Requires: python3 -m pip install cryptography

Readers only — the EUDI_PUBLIC_URL-change case:

  python3 scripts/mint-eudi-certs.py --only readers \\
      --reader-ca-key  ca-cert/ca.gbo-reader.key.pem \\
      --reader-ca-cert ca-cert/ca.gbo-reader.crt.pem \\
      --public-url     "$EUDI_PUBLIC_URL"

Everything, for a new environment:

  python3 scripts/mint-eudi-certs.py \\
      --reader-ca-key  ca-cert/ca.gbo-reader.key.pem \\
      --reader-ca-cert ca-cert/ca.gbo-reader.crt.pem \\
      --issuer-ca-key  ca-cert/ca.gbo-issuer.key.pem \\
      --issuer-ca-cert ca-cert/ca.gbo-issuer.crt.pem \\
      --public-url     "$EUDI_PUBLIC_URL" \\
      --issuer-host    issuer.simulatie.datastelsel.nl

Apply the output to .env with the companion script, then `make eudi-config`:

  python3 scripts/mint-eudi-certs.py … > certs.env
  python3 scripts/update-env.py certs.env

Appending with `>> .env` instead leaves the previous assignment of each slot
in place above the new one, which last-wins quietly tolerates until it
doesn't.
"""
import argparse, base64, datetime, json, pathlib, sys
from urllib.parse import urlparse

try:
    from cryptography import x509
    from cryptography.x509.oid import NameOID, ObjectIdentifier
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
except ImportError:
    sys.exit("this script needs `cryptography`: python3 -m pip install cryptography")

# Extension OIDs carrying the organisation / reader-auth JSON, and the EKUs
# the issuance-server checks. Taken from the certificates nl-wallet's
# wallet_ca produces (crypto/src/x509.rs, CertificateUsage).
EXT_ISSUER, EKU_ISSUER = "2.1.123.2", "1.0.18013.5.1.2"
EXT_READER, EKU_READER = "2.1.123.1", "1.0.18013.5.1.6"
EKU_STATUS = "1.3.6.1.5.5.7.3.127"

CONFIG = pathlib.Path(__file__).resolve().parent.parent / "services/eudi-issuance-server/config"

# .env slot → (auth-JSON template, EKU, extension OID). A status-list
# certificate carries no embedded JSON, only its EKU.
READERS = [
    ("EUDI_READER", "reader_auth.json.example", EKU_READER, EXT_READER),
    ("EUDI_BRP_READER", "akte_van_overlijden_reader_auth.json.example", EKU_READER, EXT_READER),
]
ISSUERS = [
    ("EUDI_ISSUER", "issuer_auth.json.example", EKU_ISSUER, EXT_ISSUER),
    ("EUDI_BRP_ISSUER", "akte_van_overlijden_issuer_auth.json.example", EKU_ISSUER, EXT_ISSUER),
    ("EUDI_STATUS", None, EKU_STATUS, None),
]


def mint(ca_key_path, ca_cert_path, host, eku_oid, ext_oid, payload, days):
    ca_key = serialization.load_pem_private_key(pathlib.Path(ca_key_path).read_bytes(), password=None)
    ca_cert = x509.load_pem_x509_certificate(pathlib.Path(ca_cert_path).read_bytes())
    key = ec.generate_private_key(ec.SECP256R1())

    builder = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, host)]))
        .issuer_name(ca_cert.subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(datetime.datetime.now(datetime.timezone.utc))
        .not_valid_after(datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(days=days))
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(host)]), critical=False)
        .add_extension(x509.ExtendedKeyUsage([ObjectIdentifier(eku_oid)]), critical=True)
    )

    if payload is not None:
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
        if len(body) > 0xFFFF:
            sys.exit(f"embedded JSON is {len(body)} bytes; this encoder only handles up to 65535")
        der = b"\x0c\x82" + len(body).to_bytes(2, "big") + body  # DER UTF8String
        builder = builder.add_extension(x509.UnrecognizedExtension(ObjectIdentifier(ext_oid), der), critical=False)

    cert = builder.sign(ca_key, hashes.SHA256())
    b64 = lambda b: base64.b64encode(b).decode()
    return (
        b64(key.private_bytes(serialization.Encoding.DER, serialization.PrivateFormat.PKCS8,
                              serialization.NoEncryption())),
        b64(cert.public_bytes(serialization.Encoding.DER)),
    )


def mint_group(specs, ca_key, ca_cert, host, days, reader_origin=None):
    out = []
    for slot, template, eku, ext in specs:
        payload = None
        if template is not None:
            payload = json.loads((CONFIG / template).read_text())
            if reader_origin is not None:
                payload["requestOriginBaseUrl"] = reader_origin
        k, c = mint(ca_key, ca_cert, host, eku, ext, payload, days)
        out += [(f"{slot}_KEY", k), (f"{slot}_CERT", c)]
    return out


def require(args, *names):
    missing = [f"--{n.replace('_', '-')}" for n in names if not getattr(args, n)]
    if missing:
        sys.exit(f"missing required argument(s) for this --only mode: {', '.join(missing)}")


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--only", choices=["all", "readers", "issuers"], default="all",
                   help="which group to mint (default: all)")
    p.add_argument("--public-url",
                   help="EUDI_PUBLIC_URL; its host becomes subject CN + DNS SAN of both reader certs")
    p.add_argument("--reader-ca-key")
    p.add_argument("--reader-ca-cert")
    p.add_argument("--reader-origin",
                   help="requestOriginBaseUrl embedded in the reader certs. The wallet checks it against the "
                        "origin it actually reached, so this must be the URL the wallet talks to — i.e. "
                        "--public-url, which is the default. Only override it when the reader request is "
                        "genuinely served from another origin. NOT EUDI_READER_ORIGIN_URL unless that happens "
                        "to equal EUDI_PUBLIC_URL; a mismatch makes the wallet abort the session with "
                        "access_denied after the certificate itself has already validated.")
    p.add_argument("--issuer-ca-key")
    p.add_argument("--issuer-ca-cert")
    p.add_argument("--issuer-host",
                   help="subject CN + DNS SAN shared by both issuer certs and the status cert, and the "
                        "credential's `iss`; defaults to the --public-url host")
    p.add_argument("--days", type=int, default=365)
    a = p.parse_args()

    do_readers = a.only in ("all", "readers")
    do_issuers = a.only in ("all", "issuers")

    reader_host = None
    if a.public_url:
        reader_host = urlparse(a.public_url).hostname
        if not reader_host:
            sys.exit(f"--public-url {a.public_url!r} has no host; pass the full URL, "
                     f"e.g. https://eudi-is.example.nl/")

    if do_readers:
        require(a, "public_url", "reader_ca_key", "reader_ca_cert")
    if do_issuers:
        require(a, "issuer_ca_key", "issuer_ca_cert")
        if not a.issuer_host and not reader_host:
            sys.exit("pass --issuer-host, or --public-url to derive it from")

    issuer_host = a.issuer_host or reader_host
    # Verbatim, including any trailing slash: the wallet compares this against
    # the origin it actually reached, and a working certificate carries the
    # public URL exactly as configured. Do not normalise it.
    reader_origin = a.reader_origin or a.public_url

    out = []
    if do_readers:
        out += mint_group(READERS, a.reader_ca_key, a.reader_ca_cert, reader_host, a.days, reader_origin)
    if do_issuers:
        out += mint_group(ISSUERS, a.issuer_ca_key, a.issuer_ca_cert, issuer_host, a.days)

    # Re-read what we just produced and assert the two invariants the
    # issuance-server checks at startup, so a mistake surfaces here rather
    # than as a crash-loop after deploy.
    minted = {k: v for k, v in out}
    subjects = {}
    for slot in [s for s, *_ in READERS + ISSUERS if f"{s}_CERT" in minted]:
        c = x509.load_der_x509_certificate(base64.b64decode(minted[f"{slot}_CERT"]))
        sans = c.extensions.get_extension_for_class(x509.SubjectAlternativeName).value.get_values_for_type(x509.DNSName)
        subjects[slot] = (c.subject.rfc4514_string(), sans)
    if do_readers:
        for slot in ("EUDI_READER", "EUDI_BRP_READER"):
            assert reader_host in subjects[slot][1], f"{slot} lacks SAN {reader_host}"
    if do_issuers:
        status = subjects["EUDI_STATUS"][0]
        for slot in ("EUDI_ISSUER", "EUDI_BRP_ISSUER"):
            if subjects[slot][0] != status:
                sys.exit(f"internal error: {slot} subject {subjects[slot][0]} != status subject {status}")

    print(f"# EUDI leaf certificates for this deployment — {a.days} days, minted with mint-eudi-certs.py.")
    if do_readers:
        print(f"# readers  CN={reader_host} (host of {a.public_url}) — the wallet checks")
        print(f"#          client_id x509_san_dns:{reader_host}, which the issuance-server and the")
        print(f"#          frontends both derive from that same URL.")
    if do_issuers:
        print(f"# issuers  CN={issuer_host} — shared by EUDI_ISSUER, EUDI_BRP_ISSUER and EUDI_STATUS")
        print(f"#          (the issuance-server requires that), and used as the credential's `iss`.")
    print("# Replace any existing lines with these names in .env, then: make eudi-config")
    for k, v in out:
        print(f"{k}={v}")

    print(f"minted {len(out) // 2} key/certificate pairs:", file=sys.stderr)
    for slot, (subject, sans) in subjects.items():
        print(f"  {slot:<16} {subject}  SAN={','.join(sans)}", file=sys.stderr)
    if do_issuers:
        print("note: a fresh EUDI_STATUS_* invalidates the status lists of already-issued "
              "credentials; re-issue them.", file=sys.stderr)


if __name__ == "__main__":
    main()
