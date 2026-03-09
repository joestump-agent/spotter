import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/docker',
        'getting-started/configuration',
        'getting-started/installation',
      ],
    },
    {
      type: 'category',
      label: 'Features',
      items: [
        'features/listening-history',
        'features/playlist-management',
        'features/vibes-engine',
        'features/metadata-enrichment',
        'features/themes',
      ],
    },
    {
      type: 'category',
      label: 'Providers',
      items: [
        'providers/navidrome',
        'providers/spotify',
        'providers/lastfm',
      ],
    },
    {
      type: 'category',
      label: 'Enrichers',
      items: [
        'enrichers/overview',
        'enrichers/musicbrainz',
        'enrichers/spotify',
        'enrichers/lastfm',
        'enrichers/fanart',
        'enrichers/lidarr',
        'enrichers/openai',
      ],
    },
    {
      type: 'category',
      label: 'Development',
      items: [
        'development/architecture',
        'development/contributing',
        'development/testing',
      ],
    },
  ],
};

export default sidebars;
