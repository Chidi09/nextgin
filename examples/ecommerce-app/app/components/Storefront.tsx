'use client'

import Link from 'next/link'
import { useCallback, useEffect, useState } from 'react'
import { ApiError, apiFetch } from '@/lib/api'
import type { Cart, CronStats, Order, Product, User } from '@/lib/types'

const fmt = (cents: number) => `$${(cents / 100).toFixed(2)}`

export default function Storefront() {
  const [products, setProducts] = useState<Product[]>([])
  const [category, setCategory] = useState('All')
  const [user, setUser] = useState<User | null>(null)
  const [cart, setCart] = useState<Cart>({ items: [], totalCents: 0 })
  const [orders, setOrders] = useState<Order[]>([])
  const [cron, setCron] = useState<CronStats>({ ticks: 0, expiredCarts: 0 })
  const [email, setEmail] = useState('demo@nextgin.dev')
  const [password, setPassword] = useState('demo1234')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const refreshAll = useCallback(async () => {
    const [prods, me, c, ords, cr] = await Promise.all([
      apiFetch<Product[]>('/api/products'),
      apiFetch<{ user: User | null }>('/api/auth/me'),
      apiFetch<Cart>('/api/cart'),
      apiFetch<Order[]>('/api/orders'),
      apiFetch<CronStats>('/api/cron/stats'),
    ])
    setProducts(prods)
    setUser(me.user)
    setCart(c)
    setOrders(ords)
    setCron(cr)
  }, [])

  useEffect(() => {
    void refreshAll()
    const id = setInterval(() => {
      void apiFetch<CronStats>('/api/cron/stats').then(setCron).catch(() => {})
    }, 10000)
    return () => clearInterval(id)
  }, [refreshAll])

  async function login(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setNotice('')
    try {
      const signedIn = await apiFetch<User>('/api/auth/login', {
        method: 'POST',
        body: JSON.stringify({ email, password }),
      })
      setUser(signedIn)
      setNotice(`Signed in as ${signedIn.email} (HttpOnly session cookie set by Go)`)
      await refreshAll()
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  async function logout() {
    await apiFetch<void>('/api/auth/logout', { method: 'POST' })
    setUser(null)
    setOrders([])
    setNotice('Signed out (session cookie cleared)')
    await refreshAll()
  }

  async function addToCart(productId: number) {
    const updated = await apiFetch<Cart>('/api/cart/items', {
      method: 'POST',
      body: JSON.stringify({ productId, qty: 1 }),
    })
    setCart(updated)
  }

  async function setQty(productId: number, qty: number) {
    const updated = await apiFetch<Cart>(`/api/cart/items/${productId}`, {
      method: qty <= 0 ? 'DELETE' : 'PATCH',
      body: qty <= 0 ? undefined : JSON.stringify({ qty }),
    })
    setCart(updated)
  }

  async function checkout() {
    setBusy(true)
    setNotice('')
    try {
      const order = await apiFetch<Order>('/api/checkout', { method: 'POST' })
      setNotice(`Order ${order.id} created (${fmt(order.totalCents)}). Click "Simulate Payment Webhook" to pay it via HMAC.`)
      await refreshAll()
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : 'Checkout failed')
    } finally {
      setBusy(false)
    }
  }

  async function simulatePayment(orderId: string) {
    try {
      const paid = await apiFetch<Order>(`/api/demo/simulate-payment/${orderId}`, { method: 'POST' })
      setNotice(`Webhook verified (HMAC-SHA256): Order ${paid.id} is now PAID`)
      await refreshAll()
    } catch (err) {
      setNotice(err instanceof ApiError ? err.message : 'Webhook failed')
    }
  }

  const categories = ['All', ...Array.from(new Set(products.map((p) => p.category)))]
  const visible = category === 'All' ? products : products.filter((p) => p.category === category)

  return (
    <>
      <header className="topbar">
        <div className="topbar-inner">
          <div className="brand">
            <span>Nextgin Store</span>
            <span className="pill">Pure Go net/http + Next.js</span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {user ? (
              <>
                <span style={{ fontSize: '0.85rem', color: 'var(--muted)' }}>{user.email}</span>
                <button className="btn-ghost" onClick={logout}>Logout</button>
              </>
            ) : (
              <span className="pill pill-warn">Guest session</span>
            )}
          </div>
        </div>
      </header>

      <main className="container">
        <section className="telemetry">
          <div>
            <strong>Go Cron (expire-carts, * * * * *)</strong> — Ticks: <strong>{cron.ticks}</strong> · Expired carts: <strong>{cron.expiredCarts}</strong>
            {cron.lastRunAt ? ` · Last run: ${new Date(cron.lastRunAt).toLocaleTimeString()}` : ' · waiting for first minute tick'}
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            <a className="pill pill-ok" href="/_nextgin/health" target="_blank" rel="noreferrer">/_nextgin/health</a>
            <a className="pill" href="/api/products" target="_blank" rel="noreferrer">Go /api/products</a>
          </div>
        </section>

        {notice && <div className="toast">{notice}</div>}

        <div className="layout-grid">
          <section>
            <div className="filters">
              {categories.map((cat) => (
                <button
                  key={cat}
                  className={`filter-btn ${category === cat ? 'active' : ''}`}
                  onClick={() => setCategory(cat)}
                >
                  {cat}
                </button>
              ))}
            </div>

            <div className="products-grid">
              {visible.map((p) => (
                <article key={p.id} className="card">
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                    <span style={{ fontSize: '0.75rem', color: 'var(--muted)' }}>{p.category}</span>
                    {p.badge && <span className="pill">{p.badge}</span>}
                  </div>
                  <Link href={`/products/${p.id}`} className="card-title" style={{ textDecoration: 'underline', textDecorationColor: 'var(--border)' }}>
                    {p.name}
                  </Link>
                  <p className="card-desc">{p.description}</p>
                  <div className="card-footer">
                    <span className="price">{fmt(p.priceCents)}</span>
                    <button className="btn-primary" onClick={() => addToCart(p.id)}>
                      Add to cart
                    </button>
                  </div>
                </article>
              ))}
            </div>
          </section>

          <aside className="sidebar">
            {!user && (
              <form className="card" onSubmit={login}>
                <h2 className="card-title">Sign in (Rate-limited Go Auth)</h2>
                <input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="Email" type="email" required />
                <input value={password} onChange={(e) => setPassword(e.target.value)} placeholder="Password" type="password" required />
                <button className="btn-primary" type="submit" disabled={busy}>
                  Sign in &amp; merge cart
                </button>
              </form>
            )}

            <div className="card">
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <h2 className="card-title">Cart</h2>
                <span className="pill">{cart.items.reduce((s, i) => s + i.qty, 0)} items</span>
              </div>
              {cart.items.length === 0 ? (
                <p className="card-desc">Your cart is empty.</p>
              ) : (
                <>
                  {cart.items.map((it) => (
                    <div key={it.productId} className="cart-item">
                      <div>
                        <div style={{ fontWeight: 600 }}>{it.name}</div>
                        <div style={{ color: 'var(--muted)', fontSize: '0.8rem' }}>{fmt(it.priceCents)} each</div>
                      </div>
                      <div className="qty-controls">
                        <button onClick={() => setQty(it.productId, it.qty - 1)}>-</button>
                        <span>{it.qty}</span>
                        <button onClick={() => setQty(it.productId, it.qty + 1)}>+</button>
                      </div>
                    </div>
                  ))}
                  <div style={{ display: 'flex', justifyContent: 'space-between', fontWeight: 700, paddingTop: 6 }}>
                    <span>Total</span>
                    <span>{fmt(cart.totalCents)}</span>
                  </div>
                  <button className="btn-primary" onClick={checkout} disabled={busy}>
                    {user ? 'Checkout' : 'Log in above to Checkout'}
                  </button>
                </>
              )}
            </div>

            <div className="card">
              <h2 className="card-title">Orders ({orders.length})</h2>
              {orders.length === 0 ? (
                <p className="card-desc">{user ? 'No orders yet.' : 'Sign in to view orders.'}</p>
              ) : (
                orders.map((o) => (
                  <div key={o.id} className="cart-item" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                      <strong>{o.id}</strong>
                      <span className={`pill ${o.status === 'paid' ? 'pill-ok' : 'pill-warn'}`}>
                        {o.status.toUpperCase()}
                      </span>
                    </div>
                    <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.82rem', color: 'var(--muted)' }}>
                      <span>{o.items.length} item(s)</span>
                      <span>{fmt(o.totalCents)}</span>
                    </div>
                    {o.status === 'pending' && (
                      <button className="btn-ok" onClick={() => simulatePayment(o.id)}>
                        Simulate Signed Payment Webhook
                      </button>
                    )}
                  </div>
                ))
              )}
            </div>
          </aside>
        </div>
      </main>
    </>
  )
}
