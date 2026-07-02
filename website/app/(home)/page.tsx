import Link from 'next/link';
import type { ReactNode } from 'react';

const TERMINAL_LINES: { text: ReactNode; cmd?: boolean }[] = [
  {
    cmd: true,
    text: (
      <>
        zester <span className="text-amber-300">&apos;web-*&apos;</span>{' '}
        state.highstate
      </>
    ),
  },
  { text: 'web-01   OK   14 states applied    1.8s' },
  { text: 'web-02   OK   14 states applied    1.9s' },
  {
    text: (
      <>
        web-03   OK   <span className="text-lime-400">2 changed</span>
        {'           2.1s'}
      </>
    ),
  },
  {
    cmd: true,
    text: (
      <>
        zester <span className="text-amber-300">&apos;G@os_family:debian&apos;</span>{' '}
        pkg.installed nginx
      </>
    ),
  },
  {
    text: (
      <>
        42 peels matched · 42 returned ·{' '}
        <span className="text-lime-400">0 failed</span>
      </>
    ),
  },
];

const NAMING_PAIRS: [string, string][] = [
  ['Minion', 'Peel'],
  ['Grains', 'Facts'],
  ['Pillar', 'Settings'],
  ['Mine', 'Basket'],
  ['Syndic', 'Leaf node'],
  ['ZeroMQ', 'NATS JetStream'],
];

const FEATURES: { title: string; body: string; href: string }[] = [
  {
    title: 'One static binary',
    body: 'No Python interpreter, no pip, no virtualenvs. Ship a single Go binary to every node and install in seconds.',
    href: '/docs/getting-started/installation',
  },
  {
    title: 'NATS JetStream built in',
    body: 'Durable events, KV storage, object storage, and horizontal scaling from a production-grade message bus — not a custom ZeroMQ protocol.',
    href: '/docs/architecture/nats',
  },
  {
    title: 'Ed25519 nkeys + TLS 1.3',
    body: 'A three-tier trust hierarchy with modern cryptography and least-privilege peel credentials. No RSA key exchange.',
    href: '/docs/reference/authentication',
  },
  {
    title: 'Built for 100k nodes',
    body: 'Commands fan out in ~250ms. Facts live in NATS KV, and superclusters with leaf nodes replace Salt syndic chains.',
    href: '/docs/operations/scaling',
  },
  {
    title: 'Jinja2-compatible templates',
    body: 'State files render with Gonja, including a Salt compatibility layer — grains, pillar, and salt[...] keep working.',
    href: '/docs/guides/templating',
  },
  {
    title: 'Fleet self-update',
    body: 'A dedicated watchdog swaps binaries atomically, soaks on readiness, and rolls back automatically. Batched fleet rollouts included.',
    href: '/docs/operations/update',
  },
];

function Terminal() {
  let line = 0;
  return (
    <div className="rounded-xl border border-fd-border bg-[var(--zester-terminal)] shadow-2xl shadow-lime-950/20 overflow-hidden text-left">
      <div className="flex items-center gap-1.5 px-4 py-3 border-b border-white/5">
        <span className="size-2.5 rounded-full bg-white/10" />
        <span className="size-2.5 rounded-full bg-white/10" />
        <span className="size-2.5 rounded-full bg-white/10" />
        <span className="ml-3 text-xs text-white/30 font-mono">
          operator@bastion
        </span>
      </div>
      <pre className="px-5 py-4 text-[13px] leading-relaxed font-mono text-neutral-300 overflow-x-auto">
        {TERMINAL_LINES.map((l, i) => {
          line += 1;
          return (
            <div
              key={i}
              className="terminal-line whitespace-pre"
              style={{ '--line': line } as React.CSSProperties}
            >
              {l.cmd ? <span className="text-lime-400">$ </span> : '  '}
              {l.text}
            </div>
          );
        })}
      </pre>
    </div>
  );
}

export default function HomePage() {
  return (
    <main className="flex-1">
      {/* Hero */}
      <section className="container mx-auto max-w-6xl px-6 pt-16 pb-20 md:pt-24">
        <div className="grid gap-12 md:grid-cols-2 md:items-center">
          <div>
            <p className="font-mono text-sm text-fd-primary mb-4">
              infrastructure automation in pure Go
            </p>
            <h1 className="font-display text-4xl md:text-5xl font-bold tracking-tight leading-[1.05] mb-6">
              Salt&apos;s mental model.
              <br />
              <span className="text-fd-primary">One static binary.</span>
            </h1>
            <p className="text-fd-muted-foreground text-lg leading-relaxed mb-8 max-w-lg">
              Zester is a SaltStack alternative powered by NATS JetStream and
              nkeys. No Python runtime, no external database, no ZeroMQ — same
              flavor family as Salt, sharper tool.
            </p>
            <div className="flex flex-wrap gap-3">
              <Link
                href="/docs/getting-started/quickstart"
                className="rounded-lg bg-fd-primary px-5 py-2.5 font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
              >
                Get started
              </Link>
              <Link
                href="/docs/getting-started/salt-compatibility"
                className="rounded-lg border border-fd-border px-5 py-2.5 font-medium text-fd-foreground transition-colors hover:bg-fd-accent"
              >
                Migrate from Salt
              </Link>
            </div>
          </div>
          <Terminal />
        </div>
      </section>

      {/* Salt -> Zester translation strip */}
      <section className="border-y border-fd-border bg-fd-card/60">
        <div className="container mx-auto max-w-6xl px-6 py-10">
          <p className="font-mono text-xs uppercase tracking-widest text-fd-muted-foreground mb-6">
            Coming from Salt? The vocabulary translates.
          </p>
          <div className="grid grid-cols-2 gap-x-8 gap-y-4 sm:grid-cols-3 lg:grid-cols-6">
            {NAMING_PAIRS.map(([salt, zester]) => (
              <div key={salt} className="swap-pair font-mono text-sm">
                <span className="swap-salt">{salt}</span>
                <span className="mx-2 text-fd-muted-foreground">→</span>
                <span className="text-fd-primary font-semibold">{zester}</span>
              </div>
            ))}
          </div>
        </div>
      </section>

      {/* Features */}
      <section className="container mx-auto max-w-6xl px-6 py-20">
        <h2 className="font-display text-2xl md:text-3xl font-bold tracking-tight mb-10">
          Everything Salt does. Less to carry.
        </h2>
        <div className="grid gap-px overflow-hidden rounded-xl border border-fd-border bg-fd-border sm:grid-cols-2 lg:grid-cols-3">
          {FEATURES.map((f) => (
            <Link
              key={f.title}
              href={f.href}
              className="group bg-fd-background p-6 transition-colors hover:bg-fd-accent"
            >
              <h3 className="font-display font-semibold mb-2 group-hover:text-fd-primary transition-colors">
                {f.title}
              </h3>
              <p className="text-sm leading-relaxed text-fd-muted-foreground">
                {f.body}
              </p>
            </Link>
          ))}
        </div>
      </section>

      {/* Bottom CTA */}
      <section className="border-t border-fd-border">
        <div className="container mx-auto max-w-6xl px-6 py-16 text-center">
          <h2 className="font-display text-2xl font-bold tracking-tight mb-3">
            Ready to peel off the Python stack?
          </h2>
          <p className="text-fd-muted-foreground mb-8">
            Spin up the Docker Compose playground — a master, five peels, and
            NATS — in one command.
          </p>
          <Link
            href="/docs/getting-started/quickstart"
            className="inline-block rounded-lg bg-fd-primary px-6 py-3 font-medium text-fd-primary-foreground transition-opacity hover:opacity-90"
          >
            Read the Quick Start
          </Link>
        </div>
      </section>
    </main>
  );
}
