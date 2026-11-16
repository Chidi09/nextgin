'use server'

import { revalidatePath } from 'next/cache'
import { apiURL } from '@/lib/api'

// Server Action: runs in the Next.js worker, persists through the Go API.
export async function addTodo(formData: FormData) {
  const title = String(formData.get('title') ?? '').trim()
  if (!title) return
  const res = await fetch(apiURL('/api/todos'), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ title }),
  })
  if (!res.ok) throw new Error(`Go API returned ${res.status}`)
  revalidatePath('/todos')
}
