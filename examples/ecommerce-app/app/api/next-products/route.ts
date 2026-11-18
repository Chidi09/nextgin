import { NextResponse } from 'next/server'
import { DatabaseSync } from 'node:sqlite'
import path from 'node:path'

export const dynamic = 'force-dynamic'

let db: DatabaseSync | null = null

function getDB(): DatabaseSync {
  if (!db) {
    const dbPath = path.resolve(process.env.SQLITE_PATH || './store.db')
    db = new DatabaseSync(dbPath, { readOnly: true })
  }
  return db
}

// Plain Next.js Route Handler querying the exact same seeded SQLite store.db as Go's /api/products.
export async function GET() {
  const rows = getDB()
    .prepare(
      'SELECT id, slug, name, category, price_cents AS priceCents, stock, badge, description FROM products ORDER BY id',
    )
    .all()
  return NextResponse.json(rows)
}
