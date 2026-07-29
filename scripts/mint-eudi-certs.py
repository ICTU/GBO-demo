#!/usr/bin/env python3
"""Mint the RvIG issuer + reader certificate pair for the akte van overlijden.

These certificates are DEPLOYMENT-BOUND and cannot be shared between
environments:

  * the wallet renders the organisation (issuer) and the purpose statement
    (reader) from a JSON blob embedded in the certificate, so the akte needs
    its own pair rather than reusing the Belastingdienst one;
  * the issuance-server refuses to start unless, per attestation type, the
    attestation certificate and the status-list certificate have the SAME
    subject — so the issuer subject must match your status certificate;
  * the reader certificate's subject/SAN is the client_id the wallet checks
    against the signed request object.

Hence: every deployment mints its own pair. Run this, paste the four values
into your .env, and re-run `make eudi-config`.

Requires: python3 -m pip install cryptography

Example:

  python3 scripts/mint-brp-certs.py \\
      --issuer-ca-key  ca-cert/ca.gbo-issuer.key.pem \\
      --issuer-ca-cert ca-cert/ca.gbo-issuer.crt.pem \\
      --reader-ca-key  ca-cert/ca.gbo-reader.key.pem \\
      --reader-ca-cert ca-cert/ca.gbo-reader.crt.pem \\
      --issuer-host    issuer-belastingdienst.gbo.b15.io \\
      --reader-host    reader-belastingdienst.gbo.b15.io \\
      --reader-origin  https://eudi-is.example.nl/ \\
      --status-cert    "$EUDI_STATUS_CERT"
"""
import argparse, base64, datetime, json, pathlib, sys

try:
    from cryptography import x509
    from cryptography.x509.oid import NameOID, ObjectIdentifier
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
except ImportError:
    sys.exit("this script needs `cryptography`: python3 -m pip install cryptography")

# Extension OIDs carrying the organisation / reader-auth JSON, and the EKUs
# the issuance-server checks. Taken from the certificates nl-wallet's
# wallet_ca produces.
EXT_ISSUER, EKU_ISSUER = "2.1.123.2", "1.0.18013.5.1.2"
EXT_READER, EKU_READER = "2.1.123.1", "1.0.18013.5.1.6"
EKU_STATUS = "1.3.6.1.5.5.7.3.127"

CONFIG = pathlib.Path(__file__).resolve().parent.parent / "services/eudi-issuance-server/config"


def mint(ca_key_path, ca_cert_path, host, ext_oid, eku_oid, payload, days):
    ca_key = serialization.load_pem_private_key(pathlib.Path(ca_key_path).read_bytes(), password=None)
    ca_cert = x509.load_pem_x509_certificate(pathlib.Path(ca_cert_path).read_bytes())
    key = ec.generate_private_key(ec.SECP256R1())

    body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()
    if len(body) > 0xFFFF:
        sys.exit(f"embedded JSON is {len(body)} bytes; this encoder only handles up to 65535")
    der = b"\x0c\x82" + len(body).to_bytes(2, "big") + body  # DER UTF8String

    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (
        x509.CertificateBuilder()
        .subject_name(x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, host)]))
        .issuer_name(ca_cert.subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now)
        .not_valid_after(now + datetime.timedelta(days=days))
        .add_extension(x509.SubjectAlternativeName([x509.DNSName(host)]), critical=False)
        .add_extension(x509.ExtendedKeyUsage([ObjectIdentifier(eku_oid)]), critical=True)
        .add_extension(x509.UnrecognizedExtension(ObjectIdentifier(ext_oid), der), critical=False)
        .sign(ca_key, hashes.SHA256())
    )
    b64 = lambda b: base64.b64encode(b).decode()
    return (
        b64(key.private_bytes(serialization.Encoding.DER, serialization.PrivateFormat.PKCS8,
                              serialization.NoEncryption())),
        b64(cert.public_bytes(serialization.Encoding.DER)),
    )


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--issuer-ca-key", required=True)
    p.add_argument("--issuer-ca-cert", required=True)
    p.add_argument("--reader-ca-key", required=True)
    p.add_argument("--reader-ca-cert", required=True)
    p.add_argument("--issuer-host", required=True,
                   help="subject CN + SAN of the issuer cert; MUST equal your status certificate's subject CN")
    p.add_argument("--reader-host", required=True,
                   help="subject CN + SAN of the reader cert; this is the client_id the wallet sees")
    p.add_argument("--reader-origin", required=True, help="requestOriginBaseUrl, e.g. https://eudi-is.example.nl/")
    p.add_argument("--status-cert", default="",
                   help="base64 DER of EUDI_STATUS_CERT; when given, the issuer subject is checked against it")
    p.add_argument("--days", type=int, default=365)
    a = p.parse_args()

    # Fail here rather than letting the issuance-server discover it at boot with
    # "attestation and status list certificate subject are different".
    if a.status_cert:
        st = x509.load_der_x509_certificate(base64.b64decode(a.status_cert.strip()))
        want = st.subject.rfc4514_string()
        if want != f"CN={a.issuer_host}":
            sys.exit(f"--issuer-host would produce subject CN={a.issuer_host}, but your status certificate's\n"
                     f"subject is {want}. The issuance-server requires them to be identical.\n"
                     f"Re-run with --issuer-host {want.removeprefix('CN=')}")
        ekus = [u.dotted_string for u in st.extensions.get_extension_for_class(x509.ExtendedKeyUsage).value]
        if EKU_STATUS not in ekus:
            sys.exit(f"the certificate passed to --status-cert has EKU {ekus}, expected {EKU_STATUS}.\n"
                     f"That looks like an issuer certificate, not a status-list certificate.")

    issuer_json = json.loads((CONFIG / "akte_van_overlijden_issuer_auth.json.example").read_text())
    reader_json = json.loads((CONFIG / "akte_van_overlijden_reader_auth.json.example").read_text())
    reader_json["requestOriginBaseUrl"] = a.reader_origin

    ik, ic = mint(a.issuer_ca_key, a.issuer_ca_cert, a.issuer_host, EXT_ISSUER, EKU_ISSUER, issuer_json, a.days)
    rk, rc = mint(a.reader_ca_key, a.reader_ca_cert, a.reader_host, EXT_READER, EKU_READER, reader_json, a.days)

    print(f"# RvIG pair for the akte van overlijden — {a.days} days, minted for this deployment.")
    print(f"# issuer subject/SAN CN={a.issuer_host}   reader subject/SAN CN={a.reader_host}")
    print("# Append to .env (replacing any existing EUDI_BRP_* lines), then: make eudi-config")
    for k, v in [("EUDI_BRP_ISSUER_KEY", ik), ("EUDI_BRP_ISSUER_CERT", ic),
                 ("EUDI_BRP_READER_KEY", rk), ("EUDI_BRP_READER_CERT", rc)]:
        print(f"{k}={v}")


if __name__ == "__main__":
    main()
