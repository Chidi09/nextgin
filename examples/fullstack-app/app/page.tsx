// Server Component, fully static: prerendered at build time and served by Go
// straight from memory without touching Node.
export default function Home() {
  return (
    <main>
      <h1>Next.js on Go + Gin</h1>
      <p>
        This page was prerendered at build time ({new Date().toISOString()}) and is served by Go. The Todos page is
        rendered by a Next.js worker over a Unix socket and talks to a Gin API.
      </p>
    </main>
  )
}
