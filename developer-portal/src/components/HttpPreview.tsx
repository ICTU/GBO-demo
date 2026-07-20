import { useState } from 'react'

type Props = { text: string }

export default function HttpPreview({ text }: Props) {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <div className="preview-h" onClick={() => setOpen((o) => !o)}>
        <span style={{ transition: 'transform .15s', transform: open ? 'rotate(90deg)' : '' }}>▸</span>
        HTTP-preview · curl-equivalent
      </div>
      {open && <pre className="codeblock">{text}</pre>}
    </div>
  )
}
