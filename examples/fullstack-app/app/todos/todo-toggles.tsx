'use client'

import { useEffect, useState } from 'react'
import type { Todo } from '@/lib/api'

// Client Component: talks to the Gin API from the browser (same origin).
export default function TodoToggles({ initial }: { initial: Todo[] }) {
  const [todos, setTodos] = useState(initial)
  useEffect(() => setTodos(initial), [initial])

  async function toggle(t: Todo) {
    const res = await fetch(`/api/todos/${t.id}`, {
      method: 'PATCH',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ done: !t.done }),
    })
    const updated: Todo = await res.json()
    setTodos((ts) => ts.map((x) => (x.id === updated.id ? updated : x)))
  }

  return (
    <ul style={{ listStyle: 'none', padding: 0 }}>
      {todos.map((t) => (
        <li key={t.id}>
          <label>
            <input type="checkbox" checked={t.done} onChange={() => toggle(t)} />{' '}
            <span style={{ textDecoration: t.done ? 'line-through' : undefined }}>{t.title}</span>
          </label>
        </li>
      ))}
    </ul>
  )
}
