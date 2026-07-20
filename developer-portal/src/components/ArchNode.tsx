import type { NodeDef } from '../data/chains'
import type { NodeState } from '../hooks/useArchState'

type Props = {
  node: NodeDef
  state: NodeState
  selected: boolean
  branch?: boolean
  onClick: () => void
}

const STATUS_GLYPH: Record<NodeState, string> = {
  green: '✓',
  red: '✕',
  yellow: '…',
  grey: '',
  'no-otel': '∅',
}

const STATUS_BG: Record<NodeState, string> = {
  green: 'var(--allow-br)',
  red: 'var(--deny-br)',
  yellow: 'var(--warn-br)',
  grey: 'transparent',
  'no-otel': 'var(--mute-br, #888)',
}

const STATUS_TITLE: Partial<Record<NodeState, string>> = {
  'no-otel': 'Deze service heeft geen OTel-instrumentatie — de span-strip kan niet zien of \'ie liep. Aanwezigheid van downstream-spans impliceert dat deze hop wel geslaagd is.',
}

export default function ArchNode({ node, state, branch, onClick }: Props) {
  const klass = `node-box ${state === 'grey' ? '' : state} ${branch ? 'branch-box' : ''}`.trim()
  const title = STATUS_TITLE[state]
  return (
    <div className={klass} onClick={(e) => { e.stopPropagation(); onClick() }} title={title}>
      {state !== 'grey' && (
        <span className="node-st" style={{ background: STATUS_BG[state] }}>
          {STATUS_GLYPH[state]}
        </span>
      )}
      <div className="node-role">{node.role}</div>
      <div className="node-name">{node.name}</div>
      <div className="node-svc">{node.svc}</div>
    </div>
  )
}
