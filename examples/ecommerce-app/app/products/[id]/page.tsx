import Link from 'next/link'

export const dynamic = 'force-dynamic'

type Product = {
  id: number
  name: string
  category: string
  priceCents: number
  stock: number
  description: string
}

export default async function ProductPage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = await params
  const apiBase = process.env.NEXTGIN_INTERNAL_URL || 'http://127.0.0.1:8080'
  const res = await fetch(`${apiBase}/api/products/${id}`, { cache: 'no-store' })

  if (!res.ok) {
    return (
      <main className="container" style={{ padding: '48px 20px' }}>
        <h1>Product not found</h1>
        <p style={{ marginTop: 12 }}>
          <Link href="/" className="pill">← Back to store</Link>
        </p>
      </main>
    )
  }

  const product: Product = await res.json()
  return (
    <main className="container" style={{ padding: '40px 20px', maxWidth: 720 }}>
      <Link href="/" className="pill">← Back to store</Link>
      <div className="card" style={{ marginTop: 20, padding: 24 }}>
        <span style={{ color: 'var(--muted)', fontSize: '0.85rem' }}>
          {product.category} · SSR via Next.js worker → Go API ({apiBase})
        </span>
        <h1 style={{ fontSize: '1.75rem' }}>{product.name}</h1>
        <p style={{ color: 'var(--muted)' }}>{product.description}</p>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 12 }}>
          <span style={{ fontSize: '1.5rem', fontWeight: 700 }}>${(product.priceCents / 100).toFixed(2)}</span>
          <span className="pill pill-ok">{product.stock} in stock</span>
        </div>
      </div>
    </main>
  )
}
