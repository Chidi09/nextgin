import { fileURLToPath } from 'node:url'
import { OPTIONS_ENV, type NextGinAdapterOptions } from './options.js'

export type { NextGinAdapterOptions } from './options.js'
export type { NextGinManifest } from './manifest.js'

/**
 * Returns the module path to pass to `adapterPath` in next.config.
 *
 * Next.js loads adapters by path (`adapterPath: string`), so options are
 * handed to the adapter module through an environment variable that the
 * build process (and its children) inherit.
 *
 * ```js
 * import { createNextGinAdapter } from '@nextgin/adapter'
 * export default { adapterPath: createNextGinAdapter() }
 * ```
 */
export function createNextGinAdapter(options: NextGinAdapterOptions = {}): string {
  process.env[OPTIONS_ENV] = JSON.stringify(options)
  return fileURLToPath(new URL('./adapter.js', import.meta.url))
}
