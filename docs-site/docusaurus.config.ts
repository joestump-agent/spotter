import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import type * as OpenApiPlugin from 'docusaurus-plugin-openapi-docs';

// ============================================================
// CONFIGURE THESE VALUES FOR YOUR PROJECT
// ============================================================
const PROJECT_TITLE = 'Spotter';
const PROJECT_TAGLINE = 'AI-Powered Playlist Generator for Navidrome';
const GITHUB_URL = 'https://github.com/joestump/spotter';
const SITE_URL = 'https://joestump.github.io';
const BASE_URL = '/spotter/';
// ============================================================

const config: Config = {
  title: PROJECT_TITLE,
  tagline: PROJECT_TAGLINE,
  favicon: 'img/favicon.ico',

  url: SITE_URL,
  baseUrl: BASE_URL,

  onBrokenLinks: 'warn',
  onBrokenMarkdownLinks: 'warn',

  markdown: {
    format: 'detect',
    mermaid: true,
  },

  themes: [
    '@docusaurus/theme-mermaid',
    'docusaurus-theme-openapi-docs',
    [
      '@easyops-cn/docusaurus-search-local',
      {
        hashed: true,
        language: ['en'],
        highlightSearchTermsOnTargetPage: true,
        explicitSearchResultPath: true,
        docsRouteBasePath: ['/', '/docs', '/api'],
        indexBlog: false,
      },
    ],
  ],

  plugins: [
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'api-docs',
        path: 'docs-api',
        routeBasePath: 'api',
        sidebarPath: './sidebars-api.ts',
      },
    ],
    [
      '@docusaurus/plugin-content-docs',
      {
        id: 'user-docs',
        path: 'docs',
        routeBasePath: 'docs',
        sidebarPath: './sidebars-docs.ts',
        editUrl: 'https://github.com/joestump/spotter/tree/main/docs-site/',
      },
    ],
    [
      'docusaurus-plugin-openapi-docs',
      {
        id: 'openapi',
        docsPluginId: 'api-docs',
        config: {
          spotter: {
            specPath: '../openapi.yaml',
            outputDir: 'docs-api',
            sidebarOptions: {
              groupPathsBy: 'tag',
              categoryLinkSource: 'tag',
            },
          },
        },
      } satisfies OpenApiPlugin.Options,
    ],
  ],

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs-generated',
          sidebarPath: './sidebars.ts',
          routeBasePath: '/',
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    image: 'img/spotter-social-card.jpg',
    colorMode: {
      defaultMode: 'dark',
      disableSwitch: false,
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: PROJECT_TITLE,
      items: [
        {
          type: 'docSidebar',
          docsPluginId: 'user-docs',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          type: 'docSidebar',
          sidebarId: 'decisionsSidebar',
          position: 'left',
          label: 'ADRs',
        },
        {
          type: 'docSidebar',
          sidebarId: 'specsSidebar',
          position: 'left',
          label: 'Specs',
        },
        {
          to: '/api/spotter',
          label: 'API',
          position: 'left',
        },
        {
          href: GITHUB_URL,
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {
              label: 'Getting Started',
              to: '/docs/getting-started/docker',
            },
            {
              label: 'Configuration',
              to: '/docs/getting-started/configuration',
            },
          ],
        },
        {
          title: 'Architecture',
          items: [
            {
              label: 'ADRs',
              to: '/decisions',
            },
            {
              label: 'Specifications',
              to: '/specs',
            },
            {
              label: 'API Reference',
              to: '/api/spotter',
            },
          ],
        },
        {
          title: 'Community',
          items: [
            {
              label: 'GitHub',
              href: GITHUB_URL,
            },
            {
              label: 'Issues',
              href: `${GITHUB_URL}/issues`,
            },
            {
              label: 'Discussions',
              href: `${GITHUB_URL}/discussions`,
            },
          ],
        },
      ],
      copyright: `Copyright ${new Date().getFullYear()} Joe Stump. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['go', 'bash', 'json', 'yaml', 'toml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
