package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Database struct {
		Driver string `mapstructure:"driver"`
		Source string `mapstructure:"source"`
	} `mapstructure:"database"`
	Server struct {
		Port string `mapstructure:"port"`
		Host string `mapstructure:"host"`
	} `mapstructure:"server"`
	Navidrome struct {
		BaseURL string `mapstructure:"base_url"`
	} `mapstructure:"navidrome"`
	Lidarr struct {
		BaseURL string `mapstructure:"base_url"`
		APIKey  string `mapstructure:"api_key"`
	} `mapstructure:"lidarr"`
	Sync struct {
		Interval string `mapstructure:"interval"`
	} `mapstructure:"sync"`
	Theme struct {
		Available string `mapstructure:"available"` // Comma-separated list of DaisyUI theme names
		Default   string `mapstructure:"default"`   // Default theme name
	} `mapstructure:"theme"`
	Spotify struct {
		ClientID     string `mapstructure:"client_id"`
		ClientSecret string `mapstructure:"client_secret"`
		RedirectURL  string `mapstructure:"redirect_url"`
	} `mapstructure:"spotify"`
	LastFM struct {
		APIKey       string `mapstructure:"api_key"`
		SharedSecret string `mapstructure:"shared_secret"`
		RedirectURL  string `mapstructure:"redirect_url"`
	} `mapstructure:"lastfm"`
	OpenAI struct {
		APIKey  string `mapstructure:"api_key"`  // OpenAI API key (required for AI enrichment)
		BaseURL string `mapstructure:"base_url"` // Base URL for API (for LiteLLM or compatible proxies)
		Model   string `mapstructure:"model"`    // Model to use for enrichment (e.g., gpt-4o, gpt-4-turbo)
	} `mapstructure:"openai"`
	Metadata struct {
		Enabled  bool   `mapstructure:"enabled"`  // Enable/disable metadata enrichment
		Interval string `mapstructure:"interval"` // Sync interval (e.g., "1h", "30m")
		Order    string `mapstructure:"order"`    // Comma-separated enricher order (e.g., "musicbrainz,navidrome,spotify,lastfm,fanart,openai")

		MusicBrainz struct {
			UserAgent string `mapstructure:"user_agent"` // Required by MusicBrainz API - should include app name and contact
		} `mapstructure:"musicbrainz"`

		Fanart struct {
			APIKey string `mapstructure:"api_key"` // Fanart.tv personal API key
		} `mapstructure:"fanart"`

		Images struct {
			Download  bool   `mapstructure:"download"`   // Whether to download images locally
			Directory string `mapstructure:"directory"`  // Directory to store downloaded images
			MaxWidth  int    `mapstructure:"max_width"`  // Maximum image width (for resizing)
			MaxHeight int    `mapstructure:"max_height"` // Maximum image height (for resizing)
		} `mapstructure:"images"`

		AI struct {
			PromptsDirectory string `mapstructure:"prompts_directory"` // Directory containing prompt templates
		} `mapstructure:"ai"`
	} `mapstructure:"metadata"`
}

// AvailableThemes returns the list of available themes parsed from the comma-separated config.
func (c *Config) AvailableThemes() []string {
	if c.Theme.Available == "" {
		return []string{"dark"}
	}
	themes := strings.Split(c.Theme.Available, ",")
	result := make([]string, 0, len(themes))
	for _, t := range themes {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

// MetadataEnricherOrder returns the list of enrichers in the configured order.
// Falls back to default order if not configured.
func (c *Config) MetadataEnricherOrder() []string {
	if c.Metadata.Order == "" {
		return []string{"lidarr", "musicbrainz", "navidrome", "spotify", "lastfm", "fanart", "openai"}
	}
	parts := strings.Split(c.Metadata.Order, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// IsOpenAIEnabled returns true if OpenAI API key is configured.
func (c *Config) IsOpenAIEnabled() bool {
	return c.OpenAI.APIKey != ""
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetEnvPrefix("SPOTTER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("sync.interval", "5m")
	v.SetDefault("theme.available", "light,dark,cupcake")
	v.SetDefault("theme.default", "dark")
	v.SetDefault("database.driver", "sqlite3")
	v.SetDefault("database.source", "file:spotter.db?cache=shared&_fk=1")

	// Set defaults for keys to ensure Viper picks up environment variables
	v.SetDefault("navidrome.base_url", "")
	v.SetDefault("lidarr.base_url", "")
	v.SetDefault("lidarr.api_key", "")
	v.SetDefault("spotify.client_id", "")
	v.SetDefault("spotify.client_secret", "")
	v.SetDefault("spotify.redirect_url", "http://127.0.0.1:8080/auth/spotify/callback")
	v.SetDefault("lastfm.api_key", "")
	v.SetDefault("lastfm.shared_secret", "")
	v.SetDefault("lastfm.redirect_url", "")

	// OpenAI defaults
	v.SetDefault("openai.api_key", "")
	v.SetDefault("openai.base_url", "https://api.openai.com/v1")
	v.SetDefault("openai.model", "gpt-4o")

	// Metadata enrichment defaults
	v.SetDefault("metadata.enabled", true)
	v.SetDefault("metadata.interval", "1h")
	v.SetDefault("metadata.order", "lidarr,musicbrainz,navidrome,spotify,lastfm,fanart,openai")
	v.SetDefault("metadata.musicbrainz.user_agent", "Spotter/1.0.0 (https://github.com/spotter)")
	v.SetDefault("metadata.fanart.api_key", "")
	v.SetDefault("metadata.images.download", true)
	v.SetDefault("metadata.images.directory", "./data/images")
	v.SetDefault("metadata.images.max_width", 1000)
	v.SetDefault("metadata.images.max_height", 1000)
	v.SetDefault("metadata.ai.prompts_directory", "./data/prompts")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	if cfg.Navidrome.BaseURL == "" {
		return nil, fmt.Errorf("navidrome.base_url is required")
	}

	if cfg.Lidarr.BaseURL == "" {
		return nil, fmt.Errorf("lidarr.base_url is required")
	}
	if cfg.Lidarr.APIKey == "" {
		return nil, fmt.Errorf("lidarr.api_key is required")
	}

	if cfg.OpenAI.APIKey == "" {
		return nil, fmt.Errorf("openai.api_key is required for AI enrichment")
	}

	return &cfg, nil
}
