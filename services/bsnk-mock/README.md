# bsnk-mock

A stand-in for BSNk, the BSN-koppelregister that Logius runs. The demo uses it
for the two transforms the chain needs: the consent portal turns a citizen's
BSN into a polymorphic identity (PI) for the dienstverlener and a portal-scoped
reference, and a source's sidecar turns a PI back into a BSN.

It is a mock, not an implementation of BSNk. It keeps no state worth keeping
and has no cryptography behind its pseudonyms.

## Logboek Dataverwerkingen

The components that call it record the call as a Dataverwerking of their own
and set `dpl.read.nextLogbookId` to this page (`LDV_BSNK_NEXT_LOGBOOK_ID`).
The read extension asks for the read API of the party that was called; for a
party without one it allows a page with contact details instead. The mock
keeps no logbook, so this page is the honest answer to where its side of a
transform can be looked up.

In a real chain the value is whatever Logius publishes for BSNk: the read API
of its logbook, or its contact point until there is one.
