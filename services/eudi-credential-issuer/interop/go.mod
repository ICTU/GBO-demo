module gbo-demo/eudi-credential-issuer/interop

go 1.26.10

require (
	gbo-demo/eudi-credential-issuer v0.0.0
	gbo-demo/oid4vc-kit v0.0.0
	github.com/idfoundry/fapigo v0.65.1
	github.com/idfoundry/oid4vcgo v0.56.0
)

require (
	github.com/fxamacker/cbor/v2 v2.9.6 // indirect
	github.com/go-jose/go-jose/v4 v4.1.5 // indirect
	github.com/x448/float16 v0.8.4 // indirect
)

replace (
	gbo-demo/eudi-credential-issuer => ../
	gbo-demo/oid4vc-kit => ../../oid4vc-kit
)
