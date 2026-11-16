import { NextResponse } from 'next/server'

// Next.js 16 "proxy" (formerly middleware). Its matcher is in the nextgin
// manifest, so Go never short-circuits these paths.
export function proxy() {
  return NextResponse.next({ headers: { 'x-next-proxy': '1' } })
}

export const config = { matcher: ['/todos/:path*'] }
