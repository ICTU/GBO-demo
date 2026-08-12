// Package main implements the BRP bron — the second source in the demo,
// next to the BD bron (services/graphql-server).
//
// It serves the BRP bronprofiel from gbo-semantiek v0.3
// (https://github.com/ICTU/gbo-semantiek/blob/main/v0.3/graphql/brp.graphql):
// persoonsgegevens uit de BRP (GBA + RNI) including the persoonslijst
// categories nationaliteit, huwelijk, verblijfstitel, gezagsverhouding and
// immigratie, plus the binnenlandse/buitenlandse verblijfadres.
//
// The demo use-case on top of it is the "Akte van overlijden": a surviving
// partner discloses her PID, and the query walks
// ingeschrevenPersoon -> heeftHuwelijk -> partners to find the deceased
// partner. Only one person in the mock data satisfies that shape.
//
// Both bronnen expose Query.ingeschrevenPersoon(bsn) — that is the
// bronprofiel-overlap in gbo-semantiek, not a copy/paste accident. They are
// separate FSC services (bri vs brp) with separate schemas, so the PDP
// selects the mirror schema per flow.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/handler"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
)

// ── Data model ────────────────────────────────────────────────────────────────
// Mirrors the BRP bronprofiel. Optional scalar fields are *string so the
// custom scalars can serialize "absent" as null; enum-typed fields are plain
// string because graphql-go's Enum.Serialize does a value lookup — an empty
// string is simply not found and serializes to null, while a nil *string
// would panic in the reflect-indirect.

// NatuurlijkPersoon — "een mens als drager van burgerlijke rechten en
// plichten". Used for huwelijkspartners and ouders; it carries no BSN and no
// persoonslijst-administratie.
type NatuurlijkPersoon struct {
	ID                      string              `json:"id"`
	IngeschrevenAls         []string            `json:"ingeschrevenAls"`
	Geslachtsnaam           string              `json:"geslachtsnaam"`
	Voornamen               *string             `json:"voornamen"`
	Voorvoegsel             *string             `json:"voorvoegsel"`
	AdellijkeTitelPredicaat *string             `json:"adellijkeTitelPredicaat"`
	Geboortedatum           *string             `json:"geboortedatum"`
	Geboorteplaats          *string             `json:"geboorteplaats"`
	Geboorteland            *string             `json:"geboorteland"`
	Geslacht                string              `json:"geslacht"`
	DatumOverlijden         *string             `json:"datumOverlijden"`
	PlaatsOverlijden        *string             `json:"plaatsOverlijden"`
	LandOverlijden          *string             `json:"landOverlijden"`
	HeeftOuder              []NatuurlijkPersoon `json:"heeftOuder"`
	HeeftHuwelijk           []Huwelijk          `json:"heeftHuwelijk"`
}

// Huwelijk — huwelijk of geregistreerd partnerschap. `partners` is
// symmetric per the upstream schema: both spouses are listed, there is no
// aangaande/partner distinction. Consumers that want "the other one" have to
// filter, which is exactly what the akte-van-overlijden use-case does.
type Huwelijk struct {
	SoortVerbintenis  string              `json:"soortVerbintenis"`
	DatumVoltrekking  *string             `json:"datumVoltrekking"`
	PlaatsVoltrekking *string             `json:"plaatsVoltrekking"`
	LandVoltrekking   *string             `json:"landVoltrekking"`
	DatumOntbinding   *string             `json:"datumOntbinding"`
	RedenOntbinding   *string             `json:"redenOntbinding"`
	Partners          []NatuurlijkPersoon `json:"partners"`
}

type Nationaliteit struct {
	Nationaliteit    string  `json:"nationaliteit"`
	RedenVerkrijging *string `json:"redenVerkrijging"`
	RedenVerlies     *string `json:"redenVerlies"`
	DatumVerkrijging *string `json:"datumVerkrijging"`
	DatumVerlies     *string `json:"datumVerlies"`
}

type Verblijfstitel struct {
	Aanduiding  string  `json:"aanduiding"`
	DatumIngang string  `json:"datumIngang"`
	DatumEinde  *string `json:"datumEinde"`
}

type Gezagsverhouding struct {
	IndicatieGezagMinderjarige string `json:"indicatieGezagMinderjarige"`
	IndicatieCuratele          string `json:"indicatieCuratele"`
}

type Immigratie struct {
	DatumVestigingInNederland *string `json:"datumVestigingInNederland"`
	LandVanwaar               *string `json:"landVanwaar"`
	LandBinnenkomst           *string `json:"landBinnenkomst"`
}

type Binnenlandsadres struct {
	VolledigAdres        *string `json:"volledigAdres"`
	AdresID              string  `json:"adresId"`
	Straatnaam           string  `json:"straatnaam"`
	Huisnummer           int     `json:"huisnummer"`
	Huisletter           *string `json:"huisletter"`
	Huisnummertoevoeging *string `json:"huisnummertoevoeging"`
	Postcode             *string `json:"postcode"`
	Woonplaatsnaam       string  `json:"woonplaatsnaam"`
}

type Buitenlandsadres struct {
	VolledigAdres *string `json:"volledigAdres"`
	AdresID       string  `json:"adresId"`
	Adresregel1   string  `json:"adresregel1"`
	Adresregel2   *string `json:"adresregel2"`
	Adresregel3   *string `json:"adresregel3"`
	Land          string  `json:"land"`
}

// Persoon is one record in the mock persoonslijst store. It carries the union
// of the Ingezetene and NietIngezetene fields; `soort` selects which of the
// two concrete GraphQL types the record materialises as. The two
// type-specific verblijfadres fields are kept apart in the mock data
// (woontOpBinnenland / woontOpBuitenland) because the GraphQL field `woontOp`
// is typed differently on each concrete type.
// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

type config struct {
	Port               string
	MockDataPath       string
	SourceMetadataPath string
}

func loadConfig() (config, error) {
	return config{
		Port:               getEnv("PORT", "4001"),
		MockDataPath:       getEnv("MOCKDATA_PATH", "mockdata/personen.json"),
		SourceMetadataPath: os.Getenv("GBO_SOURCE_METADATA_PATH"),
	}, nil
}

func loadSourceMetadataPublisher(cfg config) (*sourceMetadataPublisher, error) {
	if cfg.SourceMetadataPath == "" {
		return nil, fmt.Errorf("GBO_SOURCE_METADATA_PATH is required")
	}
	payload, err := os.ReadFile(cfg.SourceMetadataPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}
	return newSourceMetadataPublisher(payload)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type Persoon struct {
	Soort string `json:"soort"` // "Ingezetene" | "NietIngezetene"

	// Partij + NatuurlijkPersoon
	ID                      string              `json:"id"`
	IngeschrevenAls         []string            `json:"ingeschrevenAls"`
	Geslachtsnaam           string              `json:"geslachtsnaam"`
	Voornamen               *string             `json:"voornamen"`
	Voorvoegsel             *string             `json:"voorvoegsel"`
	AdellijkeTitelPredicaat *string             `json:"adellijkeTitelPredicaat"`
	Geboortedatum           *string             `json:"geboortedatum"`
	Geboorteplaats          *string             `json:"geboorteplaats"`
	Geboorteland            *string             `json:"geboorteland"`
	Geslacht                string              `json:"geslacht"`
	DatumOverlijden         *string             `json:"datumOverlijden"`
	PlaatsOverlijden        *string             `json:"plaatsOverlijden"`
	LandOverlijden          *string             `json:"landOverlijden"`
	HeeftOuder              []NatuurlijkPersoon `json:"heeftOuder"`
	HeeftHuwelijk           []Huwelijk          `json:"heeftHuwelijk"`

	// IngeschrevenPersoon
	BSN                     string            `json:"bsn"`
	TIN                     *string           `json:"tin"`
	DatumEersteInschrijving *string           `json:"datumEersteInschrijving"`
	GemeenteVanInschrijving *string           `json:"gemeenteVanInschrijving"`
	OpschortingBijhouding   string            `json:"opschortingBijhouding"`
	UitsluitingKiesrecht    string            `json:"uitsluitingKiesrecht"`
	EuropeesKiesrecht       string            `json:"europeesKiesrecht"`
	Verificatie             string            `json:"verificatie"`
	InOnderzoek             string            `json:"inOnderzoek"`
	HeeftNationaliteit      []Nationaliteit   `json:"heeftNationaliteit"`
	HeeftVerblijfstitel     *Verblijfstitel   `json:"heeftVerblijfstitel"`
	Gezag                   *Gezagsverhouding `json:"gezag"`
	HeeftImmigratie         *Immigratie       `json:"heeftImmigratie"`

	// Ingezetene
	DatumInschrijvingGemeente *string           `json:"datumInschrijvingGemeente"`
	IndicatieGeheim           string            `json:"indicatieGeheim"`
	WoontOpBinnenland         *Binnenlandsadres `json:"woontOpBinnenland"`

	// NietIngezetene
	DatumInschrijvingRNI *string           `json:"datumInschrijvingRNI"`
	LandVanVerblijf      *string           `json:"landVanVerblijf"`
	DeelnemerCodeRNI     *string           `json:"deelnemerCodeRNI"`
	WoontOpBuitenland    *Buitenlandsadres `json:"woontOpBuitenland"`
}

// ── Mock data store ───────────────────────────────────────────────────────────

// loadMockData returns the person records indexed by BSN. The store is
// returned rather than assigned to a package variable so main can hand it to
// the schema explicitly and tests can build one without touching
// process-wide state.
func loadMockData(path string) (map[string]Persoon, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var personen []Persoon
	if err := json.Unmarshal(data, &personen); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	store := make(map[string]Persoon, len(personen))
	for _, p := range personen {
		store[p.BSN] = p
	}
	slog.Info("mock data loaded", "personen", len(store))
	return store, nil
}

// ── Scalars ───────────────────────────────────────────────────────────────────

// stringScalar builds a custom scalar that behaves like a String (the mock
// does not enforce the upstream formaat-restricties, i.e. the @restrictie
// directive is not reproduced). Serialize accepts both string and *string —
// optional fields in the mock data model are pointers.
func stringScalar(name, description string) *graphql.Scalar {
	asString := func(value interface{}) interface{} {
		switch v := value.(type) {
		case string:
			if v == "" {
				return nil
			}
			return v
		case *string:
			if v != nil {
				return *v
			}
		}
		return nil
	}
	return graphql.NewScalar(graphql.ScalarConfig{
		Name:        name,
		Description: description,
		Serialize:   asString,
		ParseValue:  asString,
		ParseLiteral: func(valueAST ast.Value) interface{} {
			if sv, ok := valueAST.(*ast.StringValue); ok {
				return sv.Value
			}
			return nil
		},
	})
}

var (
	bagIDScalar           = stringScalar("BAGID", "BAG-objectidentificatie: een 16-cijferige code.")
	bsnScalar             = stringScalar("BSN", "Burgerservicenummer: 9 cijfers met geldige elfproef.")
	codelijstBRPScalar    = stringScalar("CodelijstBRP", "Waarde uit een BRP-Landelijke Tabel die niet nader is gespecificeerd.")
	codelijstINDScalar    = stringScalar("CodelijstIND", "Aanduiding verblijfsrecht conform de IND-codelijst verblijfstitels.")
	codelijstISO3166      = stringScalar("CodelijstISO3166", "Landcode conform ISO 3166-1 alpha-2.")
	codelijstLT33Scalar   = stringScalar("CodelijstLT33", "Gemeentecode conform BRP-Landelijke Tabel 33 Gemeenten.")
	codelijstLT37Scalar   = stringScalar("CodelijstLT37", "Reden opnemen/beëindigen nationaliteit conform BRP-LT 37.")
	codelijstLT38Scalar   = stringScalar("CodelijstLT38", "Adellijke titel / predicaat conform BRP-LT 38.")
	codelijstLT88Scalar   = stringScalar("CodelijstLT88", "RNI-deelnemer conform BRP-LT 88.")
	datumScalar           = stringScalar("Datum", "Kalenderdatum in ISO 8601 (jjjj-mm-dd).")
	datumIncompleetScalar = stringScalar("DatumIncompleet", "Datum waarin jaar, maand of dag onbekend kan zijn (jjjj, jjjj-mm of jjjj-mm-dd).")
	kvkNummerScalar       = stringScalar("KVKnummer", "Identificatie van een inschrijving in het Handelsregister: 8 cijfers.")
	postcodeScalar        = stringScalar("Postcode", "Nederlandse postcode in het formaat 9999 XX (NEN 5825).")
	uuidScalar            = stringScalar("UUID", "Universally Unique Identifier conform RFC 4122.")
)

// ── Enums ─────────────────────────────────────────────────────────────────────

func enumValues(pairs ...string) graphql.EnumValueConfigMap {
	m := graphql.EnumValueConfigMap{}
	for _, name := range pairs {
		m[name] = &graphql.EnumValueConfig{Value: name}
	}
	return m
}

var (
	geslachtEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "Geslacht",
		Description: "Geregistreerde geslachtsaanduiding van een natuurlijk persoon.",
		Values:      enumValues("Man", "Vrouw", "VastgesteldOnbekend"),
	})
	opschortingBijhoudingEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "OpschortingBijhouding",
		Description: "Reden waarom de bijhouding van een persoonslijst is opgeschort.",
		Values:      enumValues("Overlijden", "Emigratie", "Vermissing", "FoutAangelegdOfFraude"),
	})
	indicatieJaNeeEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "IndicatieJaNee",
		Description: "Tweewaardige ja/nee-keuzelijst; afwezigheid draagt zelf betekenis.",
		Values:      enumValues("Ja", "Nee"),
	})
	verificatieEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "Verificatie",
		Description: "Status van de meest recente identiteitsverificatie van een persoonslijst.",
		Values:      enumValues("Geverifieerd", "NietGeverifieerd", "InOnderzoek"),
	})
	indicatieEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "Indicatie",
		Description: "Logische waarde met expliciete onbekendheid (NL-conventie i.p.v. MIM-Boolean).",
		Values:      enumValues("Ja", "Nee", "Onbekend"),
	})
	soortVerbintenisEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "SoortVerbintenis",
		Description: "Juridisch type van een formele verbintenis tussen twee natuurlijke personen.",
		Values:      enumValues("Huwelijk", "GeregistreerdPartnerschap"),
	})
	indicatieGezagEnum = graphql.NewEnum(graphql.EnumConfig{
		Name:        "IndicatieGezag",
		Description: "Vorm van het ouderlijk gezag of de voogdij over een minderjarige.",
		Values: enumValues("EenhoofdigOuder", "GezamenlijkOuders",
			"GezamenlijkOuderEnDerde", "Voogdij", "TijdelijkGeenGezag"),
	})
)

// ── Object + interface types ─────────────────────────────────────────────────
// The type graph is cyclic (NatuurlijkPersoon -> Huwelijk -> NatuurlijkPersoon,
// and the interfaces' ResolveType points back at the objects), so the types
// are declared here and populated in init() using FieldsThunk.

var (
	partijInterface              *graphql.Interface
	ingeschrevenPersoonInterface *graphql.Interface
	adresInterface               *graphql.Interface

	natuurlijkPersoonType *graphql.Object
	ingezeteneType        *graphql.Object
	nietIngezeteneType    *graphql.Object
	nationaliteitType     *graphql.Object
	huwelijkType          *graphql.Object
	verblijfstitelType    *graphql.Object
	gezagsverhoudingType  *graphql.Object
	immigratieType        *graphql.Object
	binnenlandsadresType  *graphql.Object
	buitenlandsadresType  *graphql.Object
)

// mergeFields returns the union of the given field maps. Later maps win.
// Used to compose the interface hierarchy's field sets without repeating
// them per concrete type (the upstream SDL repeats them verbatim because
// GraphQL SDL has no inheritance).
func mergeFields(maps ...graphql.Fields) graphql.Fields {
	out := graphql.Fields{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func partijFields() graphql.Fields {
	return graphql.Fields{
		"ingeschrevenAls": {Type: graphql.NewList(graphql.NewNonNull(kvkNummerScalar))},
		"id":              {Type: graphql.NewNonNull(uuidScalar)},
	}
}

func natuurlijkPersoonFields() graphql.Fields {
	return mergeFields(partijFields(), graphql.Fields{
		"geslachtsnaam":           {Type: graphql.NewNonNull(graphql.String)},
		"voornamen":               {Type: graphql.String},
		"voorvoegsel":             {Type: graphql.String},
		"adellijkeTitelPredicaat": {Type: codelijstLT38Scalar},
		"geboortedatum":           {Type: datumIncompleetScalar},
		"geboorteplaats":          {Type: graphql.String},
		"geboorteland":            {Type: codelijstISO3166},
		"geslacht":                {Type: geslachtEnum},
		"datumOverlijden":         {Type: datumIncompleetScalar},
		"plaatsOverlijden":        {Type: graphql.String},
		"landOverlijden":          {Type: codelijstISO3166},
		"heeftOuder":              {Type: graphql.NewList(graphql.NewNonNull(natuurlijkPersoonType))},
		"heeftHuwelijk":           {Type: graphql.NewList(graphql.NewNonNull(huwelijkType))},
	})
}

func ingeschrevenPersoonFields() graphql.Fields {
	return mergeFields(natuurlijkPersoonFields(), graphql.Fields{
		"bsn":                     {Type: graphql.NewNonNull(bsnScalar)},
		"tin":                     {Type: graphql.String},
		"datumEersteInschrijving": {Type: datumIncompleetScalar},
		"gemeenteVanInschrijving": {Type: codelijstLT33Scalar},
		"opschortingBijhouding":   {Type: opschortingBijhoudingEnum},
		"uitsluitingKiesrecht":    {Type: indicatieJaNeeEnum},
		"europeesKiesrecht":       {Type: indicatieJaNeeEnum},
		"verificatie":             {Type: verificatieEnum},
		"inOnderzoek":             {Type: graphql.NewNonNull(indicatieEnum)},
		"heeftNationaliteit":      {Type: graphql.NewList(graphql.NewNonNull(nationaliteitType))},
		"heeftVerblijfstitel":     {Type: verblijfstitelType},
		"gezag":                   {Type: gezagsverhoudingType},
		"heeftImmigratie":         {Type: immigratieType},
	})
}

// woontOpResolver returns the type-specific verblijfadres. The mock record
// carries both variants; `soort` decides which one is in play.
func woontOpResolver(binnenland bool) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		persoon, ok := p.Source.(Persoon)
		if !ok {
			return nil, nil
		}
		if binnenland {
			if persoon.WoontOpBinnenland == nil {
				return nil, nil
			}
			return *persoon.WoontOpBinnenland, nil
		}
		if persoon.WoontOpBuitenland == nil {
			return nil, nil
		}
		return *persoon.WoontOpBuitenland, nil
	}
}

func init() {
	// Interfaces first: the objects list them in `Interfaces`, while the
	// interfaces only touch the objects inside lazily-evaluated closures
	// (FieldsThunk / ResolveType), so this order breaks the cycle.
	//
	// graphql-go has no interface-implements-interface, so Partij and
	// IngeschrevenPersoon are declared side by side and both implemented by
	// the concrete types; the upstream SDL's
	// `interface IngeschrevenPersoon implements Partij` is reproduced in
	// the PDP mirror schema (policies/dvtp/schemas/eudi/brp.graphql), which
	// gqlparser does support.
	partijInterface = graphql.NewInterface(graphql.InterfaceConfig{
		Name:        "Partij",
		Description: "Abstract supertype van natuurlijke en niet-natuurlijke personen.",
		Fields:      graphql.FieldsThunk(func() graphql.Fields { return partijFields() }),
		ResolveType: resolvePartijType,
	})
	ingeschrevenPersoonInterface = graphql.NewInterface(graphql.InterfaceConfig{
		Name:        "IngeschrevenPersoon",
		Description: "Een natuurlijke persoon die in de Basisregistratie Personen is opgenomen (ingezetene of niet-ingezetene).",
		Fields:      graphql.FieldsThunk(func() graphql.Fields { return ingeschrevenPersoonFields() }),
		ResolveType: resolvePersoonType,
	})
	adresInterface = graphql.NewInterface(graphql.InterfaceConfig{
		Name:        "Adres",
		Description: "Een leesbare aanduiding van een locatie of correspondentiepunt.",
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return graphql.Fields{"volledigAdres": {Type: graphql.String}}
		}),
		ResolveType: func(p graphql.ResolveTypeParams) *graphql.Object {
			if _, ok := p.Value.(Buitenlandsadres); ok {
				return buitenlandsadresType
			}
			return binnenlandsadresType
		},
	})

	natuurlijkPersoonType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "NatuurlijkPersoon",
		Description: "Een mens als drager van burgerlijke rechten en plichten.",
		Interfaces:  []*graphql.Interface{partijInterface},
		Fields:      graphql.FieldsThunk(func() graphql.Fields { return natuurlijkPersoonFields() }),
	})
	huwelijkType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Huwelijk",
		Description: "Een formele verbintenis tussen twee natuurlijke personen (huwelijk of geregistreerd partnerschap).",
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return graphql.Fields{
				"soortVerbintenis":  {Type: graphql.NewNonNull(soortVerbintenisEnum)},
				"datumVoltrekking":  {Type: datumIncompleetScalar},
				"plaatsVoltrekking": {Type: graphql.String},
				"landVoltrekking":   {Type: codelijstISO3166},
				"datumOntbinding":   {Type: datumIncompleetScalar},
				"redenOntbinding":   {Type: codelijstBRPScalar},
				// Symmetrisch: beide echtgenoten staan in de lijst.
				"partners": {Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(natuurlijkPersoonType)))},
			}
		}),
	})
	nationaliteitType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Nationaliteit",
		Description: "Een aan een ingeschreven persoon toegekende nationaliteit, inclusief verkrijging en verlies.",
		Fields: graphql.Fields{
			"nationaliteit":    {Type: graphql.NewNonNull(codelijstISO3166)},
			"redenVerkrijging": {Type: codelijstLT37Scalar},
			"redenVerlies":     {Type: codelijstLT37Scalar},
			"datumVerkrijging": {Type: datumIncompleetScalar},
			"datumVerlies":     {Type: datumIncompleetScalar},
		},
	})
	verblijfstitelType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Verblijfstitel",
		Description: "De verblijfsrechtelijke status van een niet-Nederlandse ingeschreven persoon.",
		Fields: graphql.Fields{
			"aanduiding":  {Type: graphql.NewNonNull(codelijstINDScalar)},
			"datumIngang": {Type: graphql.NewNonNull(datumScalar)},
			"datumEinde":  {Type: datumScalar},
		},
	})
	gezagsverhoudingType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Gezagsverhouding",
		Description: "Ouderlijk gezag bij minderjarigheid of onder-curatele-stelling bij meerderjarigheid.",
		Fields: graphql.Fields{
			"indicatieGezagMinderjarige": {Type: indicatieGezagEnum},
			"indicatieCuratele":          {Type: graphql.NewNonNull(indicatieEnum)},
		},
	})
	immigratieType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Immigratie",
		Description: "De vestiging van een ingeschreven persoon vanuit het buitenland in Nederland.",
		Fields: graphql.Fields{
			"datumVestigingInNederland": {Type: datumScalar},
			"landVanwaar":               {Type: codelijstISO3166},
			"landBinnenkomst":           {Type: codelijstISO3166},
		},
	})
	binnenlandsadresType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Binnenlandsadres",
		Description: "Een Nederlands adres opgebouwd uit een BAG-Nummeraanduiding.",
		Interfaces:  []*graphql.Interface{adresInterface},
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return graphql.Fields{
				"volledigAdres":        {Type: graphql.String},
				"adresId":              {Type: graphql.NewNonNull(bagIDScalar)},
				"straatnaam":           {Type: graphql.NewNonNull(graphql.String)},
				"huisnummer":           {Type: graphql.NewNonNull(graphql.Int)},
				"huisletter":           {Type: graphql.String},
				"huisnummertoevoeging": {Type: graphql.String},
				"postcode":             {Type: postcodeScalar},
				"woonplaatsnaam":       {Type: graphql.NewNonNull(graphql.String)},
			}
		}),
	})
	buitenlandsadresType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Buitenlandsadres",
		Description: "Een adres buiten Nederland als één tot drie vrije adresregels plus landaanduiding.",
		Interfaces:  []*graphql.Interface{adresInterface},
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return graphql.Fields{
				"volledigAdres": {Type: graphql.String},
				"adresId":       {Type: graphql.NewNonNull(uuidScalar)},
				"adresregel1":   {Type: graphql.NewNonNull(graphql.String)},
				"adresregel2":   {Type: graphql.String},
				"adresregel3":   {Type: graphql.String},
				"land":          {Type: graphql.NewNonNull(codelijstISO3166)},
			}
		}),
	})

	ingezeteneType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "Ingezetene",
		Description: "Een ingeschreven persoon met adres op het grondgebied van een Nederlandse gemeente.",
		Interfaces:  []*graphql.Interface{ingeschrevenPersoonInterface, partijInterface},
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return mergeFields(ingeschrevenPersoonFields(), graphql.Fields{
				"datumInschrijvingGemeente": {Type: datumScalar},
				"indicatieGeheim":           {Type: graphql.NewNonNull(indicatieEnum)},
				"woontOp":                   {Type: binnenlandsadresType, Resolve: woontOpResolver(true)},
			})
		}),
	})
	nietIngezeteneType = graphql.NewObject(graphql.ObjectConfig{
		Name:        "NietIngezetene",
		Description: "Een ingeschreven persoon wiens persoonslijst centraal wordt bijgehouden in de RNI.",
		Interfaces:  []*graphql.Interface{ingeschrevenPersoonInterface, partijInterface},
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return mergeFields(ingeschrevenPersoonFields(), graphql.Fields{
				"datumInschrijvingRNI": {Type: datumScalar},
				"landVanVerblijf":      {Type: codelijstISO3166},
				"deelnemerCodeRNI":     {Type: codelijstLT88Scalar},
				"woontOp":              {Type: buitenlandsadresType, Resolve: woontOpResolver(false)},
			})
		}),
	})
}

// resolvePersoonType maps a mock persoonslijst record onto its concrete
// GraphQL type. Default is Ingezetene — the demo's population is municipal.
func resolvePersoonType(p graphql.ResolveTypeParams) *graphql.Object {
	if persoon, ok := p.Value.(Persoon); ok && persoon.Soort == "NietIngezetene" {
		return nietIngezeteneType
	}
	return ingezeteneType
}

// resolvePartijType additionally covers NatuurlijkPersoon, which implements
// Partij but has no persoonslijst of its own (huwelijkspartners, ouders).
func resolvePartijType(p graphql.ResolveTypeParams) *graphql.Object {
	if _, ok := p.Value.(NatuurlijkPersoon); ok {
		return natuurlijkPersoonType
	}
	return resolvePersoonType(p)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func volledigeNaam(voornamen, voorvoegsel, geslachtsnaam string) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{voornamen, voorvoegsel, geslachtsnaam} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// akteVanOverlijden is a source-owned attestation view. It keeps selection
// semantics at the source so the published mapping can remain plain JSON
// pointers and GBO does not need BRP-specific conversion code.
func akteVanOverlijden(persoon Persoon) (map[string]any, bool) {
	var selectedHuwelijk Huwelijk
	var selectedPartner NatuurlijkPersoon
	found := false
	for _, huwelijk := range persoon.HeeftHuwelijk {
		if stringValue(huwelijk.RedenOntbinding) != "Overlijden" {
			continue
		}
		for _, partner := range huwelijk.Partners {
			if partner.ID == persoon.ID || stringValue(partner.DatumOverlijden) == "" {
				continue
			}
			if !found || stringValue(partner.DatumOverlijden) > stringValue(selectedPartner.DatumOverlijden) {
				selectedHuwelijk, selectedPartner, found = huwelijk, partner, true
			}
		}
	}
	if !found {
		return nil, false
	}
	ouders := make([]string, 0, len(selectedPartner.HeeftOuder))
	for _, ouder := range selectedPartner.HeeftOuder {
		if naam := volledigeNaam(stringValue(ouder.Voornamen), stringValue(ouder.Voorvoegsel), ouder.Geslachtsnaam); naam != "" {
			ouders = append(ouders, naam)
		}
	}
	overledeneNaam := volledigeNaam(stringValue(selectedPartner.Voornamen), stringValue(selectedPartner.Voorvoegsel), selectedPartner.Geslachtsnaam)
	echtgenootNaam := volledigeNaam(stringValue(persoon.Voornamen), stringValue(persoon.Voorvoegsel), persoon.Geslachtsnaam)
	verklaring := fmt.Sprintf("Op %s is", stringValue(selectedPartner.DatumOverlijden))
	if plaats := stringValue(selectedPartner.PlaatsOverlijden); plaats != "" {
		verklaring += " te " + plaats
	}
	verklaring += " overleden " + overledeneNaam
	if geboorte := stringValue(selectedPartner.Geboortedatum); geboorte != "" {
		verklaring += ", geboren op " + geboorte
		if plaats := stringValue(selectedPartner.Geboorteplaats); plaats != "" {
			verklaring += " te " + plaats
		}
	}
	if echtgenootNaam != "" {
		verbintenis := "gehuwd met"
		if selectedHuwelijk.SoortVerbintenis == "GeregistreerdPartnerschap" {
			verbintenis = "geregistreerd partner van"
		}
		verklaring += ", " + verbintenis + " " + echtgenootNaam
	}
	return map[string]any{
		"overledene_geslachtsnaam":  selectedPartner.Geslachtsnaam,
		"overledene_voorvoegsel":    stringValue(selectedPartner.Voorvoegsel),
		"overledene_voornamen":      stringValue(selectedPartner.Voornamen),
		"overledene_geboortedatum":  stringValue(selectedPartner.Geboortedatum),
		"overledene_geboorteplaats": stringValue(selectedPartner.Geboorteplaats),
		"overledene_geboorteland":   stringValue(selectedPartner.Geboorteland),
		"overledene_geslacht":       selectedPartner.Geslacht,
		"overledene_ouders":         strings.Join(ouders, "; "),
		"datum_overlijden":          stringValue(selectedPartner.DatumOverlijden),
		"plaats_overlijden":         stringValue(selectedPartner.PlaatsOverlijden),
		"land_overlijden":           stringValue(selectedPartner.LandOverlijden),
		"soort_verbintenis":         selectedHuwelijk.SoortVerbintenis,
		"echtgenoot_geslachtsnaam":  persoon.Geslachtsnaam,
		"echtgenoot_voorvoegsel":    stringValue(persoon.Voorvoegsel),
		"echtgenoot_voornamen":      stringValue(persoon.Voornamen),
		"verklaring_tekst":          verklaring + ".",
	}, true
}

func buildSchema(tracer trace.Tracer, store map[string]Persoon) (graphql.Schema, error) {
	akteFields := graphql.Fields{}
	for _, name := range []string{
		"overledene_geslachtsnaam", "overledene_voorvoegsel", "overledene_voornamen",
		"overledene_geboortedatum", "overledene_geboorteplaats", "overledene_geboorteland",
		"overledene_geslacht", "overledene_ouders", "datum_overlijden", "plaats_overlijden",
		"land_overlijden", "soort_verbintenis", "echtgenoot_geslachtsnaam",
		"echtgenoot_voorvoegsel", "echtgenoot_voornamen", "verklaring_tekst",
	} {
		akteFields[name] = &graphql.Field{Type: graphql.NewNonNull(graphql.String)}
	}
	akteType := graphql.NewObject(graphql.ObjectConfig{Name: "AkteVanOverlijden", Fields: akteFields})
	queryType := graphql.NewObject(graphql.ObjectConfig{
		Name:        "Query",
		Description: "Query-ingangen van dit bronprofiel.",
		Fields: graphql.Fields{
			"akteVanOverlijden": {
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(akteType))),
				Args: graphql.FieldConfigArgument{"bsn": {Type: graphql.NewNonNull(bsnScalar)}},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					bsn, _ := p.Args["bsn"].(string)
					persoon, exists := store[bsn]
					if !exists {
						return []map[string]any{}, nil
					}
					akte, ok := akteVanOverlijden(persoon)
					if !ok {
						return []map[string]any{}, nil
					}
					return []map[string]any{akte}, nil
				},
			},
			"ingeschrevenPersoon": {
				Type:        ingeschrevenPersoonInterface,
				Description: "Eén IngeschrevenPersoon op bsn.",
				Args: graphql.FieldConfigArgument{
					"bsn": {Type: graphql.NewNonNull(bsnScalar)},
				},
				Resolve: func(p graphql.ResolveParams) (interface{}, error) {
					ctx := p.Context
					_, span := tracer.Start(ctx, "resolve.ingeschrevenPersoon")
					defer span.End()

					bsn, _ := p.Args["bsn"].(string)
					persoon, exists := store[bsn]
					if !exists {
						return nil, nil
					}
					return persoon, nil
				},
			},
		},
	})

	// The concrete types are only reachable through the interfaces'
	// ResolveType, so register them explicitly — otherwise
	// `... on Ingezetene` fragments fail with 'Unknown type'.
	return graphql.NewSchema(graphql.SchemaConfig{
		Query: queryType,
		Types: []graphql.Type{
			ingezeteneType,
			nietIngezeteneType,
			natuurlijkPersoonType,
			binnenlandsadresType,
			buitenlandsadresType,
			adresInterface,
			partijInterface,
		},
	})
}

// ── OTel setup ────────────────────────────────────────────────────────────────

func initTracer(ctx context.Context) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317"
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "brp-graphql-server"
	}

	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(100*time.Millisecond)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// ── HTTP server ───────────────────────────────────────────────────────────────

// newMux builds the routing tree for the BRP GraphQL server. Extracted from
// main so integration tests can wire the handlers to an httptest.Server
// without starting the real listener.
func newMux(schema *graphql.Schema, tracer trace.Tracer, publisher ...http.Handler) *http.ServeMux {
	mux := http.NewServeMux()

	// UI off. The one graphql-go/handler bundles is GraphiQL 0.11 from an
	// unpinned CDN; the playground lives in the developer-portal (/playground
	// there, one page for every bron) and queries this endpoint over its own
	// proxy. Introspection needs no flag — graphql-go answers __schema/__type
	// unconditionally, which is what the portal's Schema tab reads.
	gqlHandler := handler.New(&handler.Config{
		Schema:   schema,
		Pretty:   true,
		GraphiQL: false,
	})

	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, span := tracer.Start(r.Context(), "graphql.query")
		defer span.End()
		// The FSC-Inway proxies the Fsc-Transaction-Id through to the
		// backend. Store it as a span attribute so the dev-portal can use a
		// tag lookup to correlate the graphql trace with the adapter and pdp
		// traces (traceparent propagation is broken across the FSC hop).
		if txID := r.Header.Get("Fsc-Transaction-Id"); txID != "" {
			span.SetAttributes(attribute.String("gbo.fsc.transaction_id", txID))
		}
		gqlHandler.ServeHTTP(w, r.WithContext(r.Context()))
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if len(publisher) == 1 && publisher[0] != nil {
		mux.Handle("/.well-known/gbo", publisher[0])
	}

	return mux
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "brp-graphql-server"))

	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}

	ctx := context.Background()
	shutdown, err := initTracer(ctx)
	if err != nil {
		slog.Warn("tracer init failed", "err", err.Error())
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(shutdownCtx); err != nil {
				slog.Error("tracer shutdown error", "err", err.Error())
			}
		}()
	}

	tracer := otel.Tracer("brp-graphql-server")

	store, err := loadMockData(cfg.MockDataPath)
	if err != nil {
		fatal("loading mock data", err)
	}

	schema, err := buildSchema(tracer, store)
	if err != nil {
		fatal("building schema", err)
	}
	publisher, err := loadSourceMetadataPublisher(cfg)
	if err != nil {
		fatal("loading source metadata publisher", err)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(newMux(&schema, tracer, publisher)), "brp-graphql-server"),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	slog.Info("listening", "addr", srv.Addr)
	fatal("listen and serve", srv.ListenAndServe())
}
