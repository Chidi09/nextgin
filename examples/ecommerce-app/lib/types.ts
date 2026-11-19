export type Product = {
  id: number
  slug: string
  name: string
  category: string
  priceCents: number
  stock: number
  description: string
  badge?: string
}

export type User = {
  id?: number
  email: string
  name: string
}

export type CartItem = {
  productId: number
  name: string
  priceCents: number
  qty: number
}

export type Cart = {
  items: CartItem[]
  totalCents: number
}

export type Order = {
  id: string
  userEmail: string
  items: CartItem[]
  totalCents: number
  status: 'pending' | 'paid'
  createdAt: string
  paidAt?: string
}

export type CronStats = {
  ticks: number
  expiredCarts: number
  lastRunAt?: string
}
