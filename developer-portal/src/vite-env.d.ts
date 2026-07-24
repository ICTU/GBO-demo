/// <reference types="vite/client" />

interface Window {
  __GBO_RUNTIME_CONFIG__?: {
    eudiClientId?: string
    eudiPublicUrl?: string
    jaegerPublicUrl?: string
    grafanaPublicUrl?: string
  }
}
