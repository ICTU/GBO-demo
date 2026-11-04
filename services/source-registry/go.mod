module gbo-demo/source-registry

go 1.25.0

require (
	gbo-demo/asi-mapping v0.0.0
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/vektah/gqlparser/v2 v2.5.34 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

// The asi capability in a release is read with the module that defines it.
replace gbo-demo/asi-mapping => ../asi-mapping
