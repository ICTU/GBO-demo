import { useState } from 'react'
import { hlJSON } from '../util/highlight'

type Props = {
  label?: string
  method?: string
  url?: string
  status?: number
  requestBody?: unknown
  responseBody?: unknown
}

export default function NodeIO({ method, url, status, requestBody, responseBody }: Props) {
  const [openReq, setOpenReq] = useState(false)
  const [openRes, setOpenRes] = useState(false)
  const reqText = requestBody != null
    ? JSON.stringify({ method, url, body: requestBody }, null, 2)
    : url ? JSON.stringify({ method, url }, null, 2) : null
  const resText = responseBody != null ? JSON.stringify(responseBody, null, 2) : null
  return (
    <div className="io-wrap">
      {reqText && (
        <>
          <button className={`io-h${openReq ? ' on' : ''}`} onClick={() => setOpenReq((v) => !v)}>
            <span className={`io-chev${openReq ? ' open' : ''}`}>▸</span>
            Request
          </button>
          {openReq && <pre className="io-json" dangerouslySetInnerHTML={{ __html: hlJSON(reqText) }} />}
        </>
      )}
      {resText && (
        <>
          <button className={`io-h${openRes ? ' on' : ''}`} onClick={() => setOpenRes((v) => !v)}>
            <span className={`io-chev${openRes ? ' open' : ''}`}>▸</span>
            Response
            {status != null && <span className="io-http">HTTP {status}</span>}
          </button>
          {openRes && <pre className="io-json" dangerouslySetInnerHTML={{ __html: hlJSON(resText) }} />}
        </>
      )}
    </div>
  )
}
