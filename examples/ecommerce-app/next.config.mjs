import { createNextGinAdapter } from '@nextgin/adapter'

/** @type {import('next').NextConfig} */
export default {
  adapterPath: createNextGinAdapter(),
}
