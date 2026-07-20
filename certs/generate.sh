#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"

echo "Generating self-signed CA and server certificates for demo mTLS..."

# CA
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$DIR/ca-key.pem" \
  -out "$DIR/ca.pem" \
  -days 3650 \
  -subj "/CN=GBO Demo CA/O=GBO Demo/C=NL"

# Server cert
openssl req -newkey rsa:2048 -nodes \
  -keyout "$DIR/server-key.pem" \
  -out "$DIR/server.csr" \
  -subj "/CN=fsc-mock/O=GBO Demo/C=NL"

openssl x509 -req -in "$DIR/server.csr" \
  -CA "$DIR/ca.pem" -CAkey "$DIR/ca-key.pem" \
  -CAcreateserial \
  -out "$DIR/server.pem" \
  -days 3650

# Client cert (hypotheekverlener)
openssl req -newkey rsa:2048 -nodes \
  -keyout "$DIR/client-key.pem" \
  -out "$DIR/client.csr" \
  -subj "/CN=00000001234567890000/O=Demo Hypotheekverlener/C=NL"

openssl x509 -req -in "$DIR/client.csr" \
  -CA "$DIR/ca.pem" -CAkey "$DIR/ca-key.pem" \
  -CAcreateserial \
  -out "$DIR/client.pem" \
  -days 3650

rm -f "$DIR"/*.csr "$DIR"/*.srl

echo "Certificates generated in $DIR"
