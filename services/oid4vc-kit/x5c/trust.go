package x5c

// Trust is a current set of certificate roots and their optional DCQL AKI
// discovery hints. Consumers own the port that supplies this value.
type Trust struct {
	Anchors     []Anchors
	Authorities []string
}
