import { Suspense } from 'react'

export const dynamic = 'force-dynamic'

async function Slow() {
  await new Promise((r) => setTimeout(r, 1500))
  return <p id="slow">Loaded after 1.5s, streamed through Go without buffering.</p>
}

export default function StreamPage() {
  return (
    <main>
      <h1>Suspense streaming</h1>
      <Suspense fallback={<p id="fallback">Loading…</p>}>
        <Slow />
      </Suspense>
    </main>
  )
}
