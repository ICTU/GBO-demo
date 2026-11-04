// A remote source is read from a consumer's authorized view of its Manager,
// rather than by distributing the source's internal private key.
export function isFscSourcePeer(peer: string): boolean {
  return peer === 'bd' || ['bd-via-hv', 'bd-via-edi', 'bd-via-asip'].includes(peer)
}
