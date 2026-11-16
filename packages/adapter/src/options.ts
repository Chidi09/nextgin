export interface NextGinAdapterOptions {
  /** Output directory, relative to the Next.js project dir. Default `.nextgin`. */
  outDir?: string
  /**
   * Copy the standalone server + worker into the output dir. Disable only if
   * you serve exclusively static/prerendered content. Default `true`.
   */
  includeServer?: boolean
}

export const OPTIONS_ENV = 'NEXTGIN_ADAPTER_OPTIONS'

export function readOptions(): Required<NextGinAdapterOptions> {
  let parsed: NextGinAdapterOptions = {}
  const raw = process.env[OPTIONS_ENV]
  if (raw) {
    try {
      parsed = JSON.parse(raw)
    } catch {
      throw new Error(`[nextgin] invalid ${OPTIONS_ENV}: ${raw}`)
    }
  }
  return { outDir: parsed.outDir ?? '.nextgin', includeServer: parsed.includeServer ?? true }
}
