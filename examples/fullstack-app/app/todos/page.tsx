import { apiURL, type Todo } from '@/lib/api'
import { addTodo } from './actions'
import TodoToggles from './todo-toggles'

// Rendered per request by a Next.js worker; data comes from the Gin API.
export const dynamic = 'force-dynamic'

export default async function TodosPage() {
  const res = await fetch(apiURL('/api/todos'), { cache: 'no-store' })
  const todos: Todo[] = await res.json()

  return (
    <main>
      <h1>Todos</h1>
      <p>Server-rendered from <code>GET /api/todos</code> (Go). Add uses a Server Action; toggling is a Client Component calling the Go API directly.</p>
      <form action={addTodo} style={{ display: 'flex', gap: '.5rem', marginBottom: '1rem' }}>
        <input name="title" placeholder="New todo" required />
        <button type="submit">Add (Server Action)</button>
      </form>
      <TodoToggles initial={todos} />
    </main>
  )
}
