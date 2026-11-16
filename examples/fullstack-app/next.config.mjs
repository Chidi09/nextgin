import { createNextGinAdapter } from '@nextgin/adapter'

/** @type {import('next').NextConfig} */
export default {
  adapterPath: createNextGinAdapter(),
  async headers() {
    // Header-only rules without conditions are applied by Go on its fast path.
    return [{ source: '/about', headers: [{ key: 'x-powered-by-go', value: 'gin' }] }]
  },
  async redirects() {
    return [{ source: '/home', destination: '/', permanent: false }]
  },
}
