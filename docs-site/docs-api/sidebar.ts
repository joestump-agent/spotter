import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "spotter-api",
    },
    {
      type: "category",
      label: "Authentication",
      link: {
        type: "doc",
        id: "authentication",
      },
      items: [
        {
          type: "doc",
          id: "get-login-page",
          label: "Login page",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "post-login",
          label: "Submit login credentials",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "get-logout",
          label: "Log out",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-spotify-login",
          label: "Initiate Spotify OAuth",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-spotify-callback",
          label: "Spotify OAuth callback",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-lastfm-login",
          label: "Initiate Last.fm OAuth",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-lastfm-callback",
          label: "Last.fm OAuth callback",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Home",
      link: {
        type: "doc",
        id: "home",
      },
      items: [
        {
          type: "doc",
          id: "get-home",
          label: "Home dashboard",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "post-generate",
          label: "Generate a playlist",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Library",
      link: {
        type: "doc",
        id: "library",
      },
      items: [
        {
          type: "doc",
          id: "list-artists",
          label: "List artists",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-artist",
          label: "Get artist detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-artist-image",
          label: "Get artist image",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "regenerate-artist-ai",
          label: "Regenerate AI metadata for artist",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "find-similar-artists",
          label: "Find similar artists",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "create-mixtape-from-artist",
          label: "Create mixtape from artist",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "list-albums",
          label: "List albums",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-album",
          label: "Get album detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-album-image",
          label: "Get album image",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "regenerate-album-ai",
          label: "Regenerate AI metadata for album",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "create-mixtape-from-album",
          label: "Create mixtape from album",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "list-tracks",
          label: "List tracks",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-track",
          label: "Get track detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "regenerate-track-ai",
          label: "Regenerate AI metadata for track",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "get-recent",
          label: "Recent listens",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Playlists",
      link: {
        type: "doc",
        id: "playlists",
      },
      items: [
        {
          type: "doc",
          id: "list-playlists",
          label: "List playlists",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-playlist",
          label: "Get playlist detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-playlist-sync-status",
          label: "Get playlist sync status",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-playlist-sync-progress",
          label: "Get playlist sync progress",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "toggle-playlist-sync",
          label: "Toggle playlist sync",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-playlist",
          label: "Sync playlist",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "rebuild-playlist-sync",
          label: "Rebuild playlist sync",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "generate-playlist-metadata",
          label: "Generate AI metadata for playlist",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "generate-playlist-artwork",
          label: "Generate AI artwork for playlist",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Vibes",
      link: {
        type: "doc",
        id: "vibes",
      },
      items: [
        {
          type: "doc",
          id: "list-d-js",
          label: "List DJ profiles",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "create-dj",
          label: "Create DJ profile",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "get-dj",
          label: "Get DJ profile detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "update-dj",
          label: "Update DJ profile",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "delete-dj",
          label: "Delete DJ profile",
          className: "api-method delete",
        },
        {
          type: "doc",
          id: "get-dj-genre-suggestions",
          label: "Get genre suggestions",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "get-dj-artist-suggestions",
          label: "Get artist suggestions",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "list-mixtapes",
          label: "List mixtapes",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "create-mixtape",
          label: "Create mixtape",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "get-mixtape",
          label: "Get mixtape detail",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "update-mixtape",
          label: "Update mixtape",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "delete-mixtape",
          label: "Delete mixtape",
          className: "api-method delete",
        },
        {
          type: "doc",
          id: "toggle-mixtape-sync",
          label: "Toggle mixtape sync",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "generate-mixtape-tracks",
          label: "Generate mixtape tracks",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Preferences",
      link: {
        type: "doc",
        id: "preferences",
      },
      items: [
        {
          type: "doc",
          id: "get-appearance-preferences",
          label: "Get appearance preferences",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "post-appearance-preferences",
          label: "Update appearance preferences",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "get-provider-preferences",
          label: "Get provider preferences",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "sync-navidrome",
          label: "Sync Navidrome library",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "rebuild-navidrome",
          label: "Rebuild Navidrome library",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-spotify",
          label: "Sync Spotify data",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "rebuild-spotify",
          label: "Rebuild Spotify data",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "disconnect-spotify",
          label: "Disconnect Spotify",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-lastfm",
          label: "Sync Last.fm data",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "rebuild-lastfm",
          label: "Rebuild Last.fm data",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "disconnect-lastfm",
          label: "Disconnect Last.fm",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Tasks",
      link: {
        type: "doc",
        id: "tasks",
      },
      items: [
        {
          type: "doc",
          id: "get-tasks-page",
          label: "Get background tasks page",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "sync-listens-task",
          label: "Sync listens task",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-playlists-task",
          label: "Sync playlists task",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "enrich-metadata-task",
          label: "Enrich metadata task",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-artist-images-task",
          label: "Sync artist images task",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "sync-album-images-task",
          label: "Sync album images task",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "reset-tasks",
          label: "Reset all tasks",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "cleanup-tasks",
          label: "Cleanup task data",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Events",
      link: {
        type: "doc",
        id: "events",
      },
      items: [
        {
          type: "doc",
          id: "get-events",
          label: "Server-Sent Events stream",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
