/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Admin API taban yolu — nginx proxy arkasında varsayılan "/api". */
  readonly VITE_API_BASE_URL?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
