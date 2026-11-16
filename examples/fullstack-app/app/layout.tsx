import Link from 'next/link'
import type { ReactNode } from 'react'

export const metadata = { title: 'nextgin fullstack example' }

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body style={{ fontFamily: 'system-ui, sans-serif', maxWidth: 720, margin: '2rem auto', padding: '0 1rem' }}>
        <nav style={{ display: 'flex', gap: '1rem', marginBottom: '2rem' }}>
          <Link href="/">Home</Link>
          <Link href="/about">About (static)</Link>
          <Link href="/todos">Todos (SSR + Go API)</Link>
          <Link href="/stream">Streaming</Link>
        </nav>
        {children}
      </body>
    </html>
  )
}
