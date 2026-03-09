import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';

const features = [
  {
    icon: '🎵',
    title: 'Playlist Sync',
    description:
      'Automatically syncs your Navidrome playlists with listening data, keeping your music library in perfect harmony.',
    link: '/specs/playlist-sync-navidrome',
    linkText: 'View Spec →',
  },
  {
    icon: '🤖',
    title: 'AI Mixtape Engine',
    description:
      'Generate personalized mixtapes using LLM-powered curation — like having a DJ who actually knows your taste.',
    link: '/specs/vibes-ai-mixtape-engine',
    linkText: 'View Spec →',
  },
  {
    icon: '🎤',
    title: 'Similar Artists',
    description:
      'Discover new music through intelligent artist similarity analysis powered by your own listening history.',
    link: '/specs/similar-artists-discovery',
    linkText: 'View Spec →',
  },
  {
    icon: '🔐',
    title: 'Secure Auth',
    description:
      'Navidrome-backed authentication with AES-256-GCM encryption, secure sessions, and key rotation support.',
    link: '/specs/user-authentication',
    linkText: 'View Spec →',
  },
  {
    icon: '📡',
    title: 'Live Event Bus',
    description:
      'Real-time SSE event streaming keeps every connected client in sync without polling overhead.',
    link: '/specs/event-bus-sse',
    linkText: 'View Spec →',
  },
  {
    icon: '📊',
    title: 'Observability',
    description:
      'Structured metrics and logging throughout — track sync performance, LLM costs, and system health.',
    link: '/specs/observability',
    linkText: 'View Spec →',
  },
];

const adrs = [
  {
    icon: '⚡',
    title: 'HTMX + Templ',
    description: 'Server-driven UI without the SPA complexity.',
    link: '/decisions/ADR-0001-htmx-templ-server-driven-ui',
  },
  {
    icon: '🗄️',
    title: 'SQLite + Ent ORM',
    description: 'Embedded database with type-safe code generation.',
    link: '/decisions/ADR-0003-sqlite-embedded-database',
  },
  {
    icon: '🧠',
    title: 'LiteLLM Backend',
    description: 'OpenAI-compatible API for flexible AI provider support.',
    link: '/decisions/ADR-0008-openai-api-litellm-compatible-llm-backend',
  },
  {
    icon: '🎨',
    title: 'Tailwind + DaisyUI',
    description: 'Utility-first styling with component primitives.',
    link: '/decisions/ADR-0011-tailwind-daisyui-ui-styling',
  },
];

function FeatureCard({ icon, title, description, link, linkText }: typeof features[0]) {
  return (
    <div className="col col--4" style={{ marginBottom: '1.5rem' }}>
      <Link to={link} className="feature-card" style={{ textDecoration: 'none' }}>
        <span className="feature-card__icon">{icon}</span>
        <div className="feature-card__title">{title}</div>
        <p className="feature-card__description">{description}</p>
        <span className="feature-card__link">{linkText}</span>
      </Link>
    </div>
  );
}

function ADRCard({ icon, title, description, link }: typeof adrs[0]) {
  return (
    <div className="col col--3" style={{ marginBottom: '1rem' }}>
      <Link to={link} className="feature-card" style={{ textDecoration: 'none' }}>
        <span className="feature-card__icon" style={{ fontSize: '1.75rem' }}>{icon}</span>
        <div className="feature-card__title" style={{ fontSize: '0.95rem' }}>{title}</div>
        <p className="feature-card__description" style={{ fontSize: '0.85rem' }}>{description}</p>
      </Link>
    </div>
  );
}

export default function Home(): React.ReactElement {
  const { siteConfig } = useDocusaurusContext();

  return (
    <Layout
      title={siteConfig.title}
      description={siteConfig.tagline}
    >
      {/* Hero */}
      <header className="hero--spotter">
        <div className="container">
          <span className="hero__logo">🎧</span>
          <h1 className="hero__title--spotter">
            <span>Spotter</span>
          </h1>
          <p className="hero__subtitle--spotter">
            AI-powered music discovery and playlist sync for{' '}
            <strong>Navidrome</strong>.
            Aggregate listening history, generate mixtapes with AI DJs, and enrich your music library.
          </p>
          <div className="hero__cta">
            <Link className="button button--lg button--spotify" to="/docs/getting-started/docker">
              Get Started
            </Link>
            <Link className="button button--lg button--spotify-outline" to="/docs">
              Documentation
            </Link>
            <Link className="button button--lg button--spotify-outline" to="/api/spotter">
              API Reference
            </Link>
            <Link
              className="button button--lg button--spotify-outline"
              to="https://github.com/joestump/spotter"
            >
              GitHub
            </Link>
          </div>
        </div>
      </header>

      {/* AI Disclosure */}
      <div className="ai-disclosure">
        <div className="container">
          <span className="ai-disclosure__text">
            🤖{' '}
            <strong>Built by AI.</strong>{' '}
            Spotter was written almost entirely by{' '}
            <a
              href="https://claude.ai/claude-code"
              target="_blank"
              rel="noopener noreferrer"
              className="ai-disclosure__link"
            >
              Claude Code
            </a>
            {' '}(Anthropic). Use at your own risk — no warranty provided.{' '}
            <a
              href="https://github.com/joestump/spotter#ai-disclosure"
              target="_blank"
              rel="noopener noreferrer"
              className="ai-disclosure__learn-more"
            >
              Learn more
            </a>
          </span>
        </div>
      </div>

      {/* Stats */}
      <div className="stats-bar">
        <div className="container">
          <div className="row" style={{ justifyContent: 'center' }}>
            <div className="col col--2">
              <div className="stat-item">
                <div className="stat-number">13</div>
                <div className="stat-label">Specs</div>
              </div>
            </div>
            <div className="col col--2">
              <div className="stat-item">
                <div className="stat-number">22</div>
                <div className="stat-label">ADRs</div>
              </div>
            </div>
            <div className="col col--2">
              <div className="stat-item">
                <div className="stat-number">Go</div>
                <div className="stat-label">Language</div>
              </div>
            </div>
            <div className="col col--2">
              <div className="stat-item">
                <div className="stat-number">v0.1</div>
                <div className="stat-label">Release</div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <main>
        {/* Quick Start */}
        <section className="features-section">
          <div className="container">
            <h2 className="section-title">Quick Start</h2>
            <p className="section-subtitle">Up and running in under a minute with Docker</p>
            <div className="row" style={{ justifyContent: 'center' }}>
              <div className="col col--8">
                <div className="quickstart-code">
                  <pre>
                    <code>
{`# Download the compose file
curl -o docker-compose.yml \\
  https://raw.githubusercontent.com/joestump/spotter/main/docker-compose.postgres.yml

# Edit with your Navidrome URL, API keys, etc.
$EDITOR docker-compose.yml

# Start Spotter + PostgreSQL
docker compose up -d`}
                    </code>
                  </pre>
                </div>
                <p style={{ textAlign: 'center', marginTop: '1.5rem' }}>
                  <Link to="/docs/getting-started/docker" style={{ color: '#1DB954', fontWeight: 600 }}>
                    View full installation guide →
                  </Link>
                </p>
              </div>
            </div>
          </div>
        </section>

        {/* Features */}
        <section className="features-section">
          <div className="container">
            <h2 className="section-title">What Spotter Does</h2>
            <p className="section-subtitle">
              Self-hosted music intelligence — runs alongside your Navidrome instance
            </p>
            <div className="row">
              {features.map((f) => (
                <FeatureCard key={f.title} {...f} />
              ))}
            </div>
          </div>
        </section>

        {/* ADRs Highlight */}
        <section className="features-section-alt">
          <div className="container">
            <h2 className="section-title">Key Architecture Decisions</h2>
            <p className="section-subtitle">
              22 documented decisions covering every major technical choice
            </p>
            <div className="row">
              {adrs.map((a) => (
                <ADRCard key={a.title} {...a} />
              ))}
            </div>
            <div style={{ textAlign: 'center', marginTop: '2rem' }}>
              <Link className="button button--lg button--spotify" to="/decisions">
                All 22 ADRs →
              </Link>
            </div>
          </div>
        </section>

        {/* Tech Stack */}
        <section className="features-section">
          <div className="container">
            <h2 className="section-title">Built With</h2>
            <p className="section-subtitle">A modern Go stack — small binary, multiple database backends</p>
            <div className="row" style={{ justifyContent: 'center' }}>
              {[
                { icon: '🐹', label: 'Go', sub: 'Language' },
                { icon: '🌐', label: 'HTMX', sub: 'UI' },
                { icon: '🗄️', label: 'SQLite / Postgres / MariaDB', sub: 'Database' },
                { icon: '🐳', label: 'Docker', sub: 'Deployment' },
                { icon: '🎵', label: 'Navidrome', sub: 'Music Server' },
                { icon: '🤖', label: 'LiteLLM', sub: 'AI Backend' },
              ].map(({ icon, label, sub }) => (
                <div key={label} className="col col--2" style={{ marginBottom: '1rem' }}>
                  <div className="tech-card">
                    <span className="tech-card__icon">{icon}</span>
                    <div className="tech-card__label">{label}</div>
                    <div className="tech-card__sub">{sub}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </section>
      </main>
    </Layout>
  );
}
