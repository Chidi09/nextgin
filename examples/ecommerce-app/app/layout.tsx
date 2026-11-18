import type { Metadata } from 'next'
import './globals.css'

export const metadata: Metadata = {
  title: 'Nextgin Store — Pure Go + Next.js Ecommerce',
  description: 'Lightweight pure-Go API & cron backend with Next.js App Router storefront',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  )
}
