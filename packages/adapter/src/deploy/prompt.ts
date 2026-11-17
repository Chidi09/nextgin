import readline from 'node:readline'
import { Writable } from 'node:stream'

export interface Prompter {
  confirm(q: string, def: boolean): Promise<boolean>
  secret(q: string): Promise<string>
  readonly interactive: boolean
}

/**
 * Creates a terminal prompter using `node:readline` on `process.stdin` / `process.stdout`.
 */
export function createPrompter(): Prompter {
  const interactive = Boolean(process.stdin.isTTY && process.stdout.isTTY)

  return {
    interactive,

    async confirm(q: string, def: boolean): Promise<boolean> {
      if (!interactive) {
        return def
      }

      const hint = def ? '[Y/n]' : '[y/N]'
      const rl = readline.createInterface({
        input: process.stdin,
        output: process.stdout,
      })

      try {
        while (true) {
          const answer = await new Promise<string>((resolve, reject) => {
            const onSigint = () => {
              process.stdout.write('\n')
              reject(new Error('nextgin deploy: aborted'))
            }
            rl.once('SIGINT', onSigint)
            rl.question(`${q} ${hint} `, (ans) => {
              rl.removeListener('SIGINT', onSigint)
              resolve(ans)
            })
          })

          const trimmed = answer.trim().toLowerCase()
          if (trimmed === '') return def
          if (trimmed === 'y' || trimmed === 'yes') return true
          if (trimmed === 'n' || trimmed === 'no') return false
        }
      } finally {
        rl.close()
      }
    },

    async secret(q: string): Promise<string> {
      if (!interactive) {
        throw new Error('nextgin deploy: cannot prompt for secret in non-interactive mode')
      }

      let muted = false
      const mutedOut = new Writable({
        write(chunk, encoding, callback) {
          if (!muted) {
            process.stdout.write(chunk, encoding)
          }
          callback()
        },
      })

      const rl = readline.createInterface({
        input: process.stdin,
        output: mutedOut,
        terminal: true,
      })

      try {
        return await new Promise<string>((resolve, reject) => {
          const onSigint = () => {
            muted = false
            process.stdout.write('\n')
            reject(new Error('nextgin deploy: aborted'))
          }
          rl.once('SIGINT', onSigint)
          process.stdout.write(`${q}: `)
          muted = true
          rl.question('', (ans) => {
            muted = false
            rl.removeListener('SIGINT', onSigint)
            process.stdout.write('\n')
            resolve(ans.trim())
          })
        })
      } finally {
        muted = false
        rl.close()
      }
    },
  }
}
