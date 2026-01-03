package config

import (
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
}

func Load() (*Config, error) {
	v := viper.New()

	v.SetEnvPrefix("SPOTTER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Defaults
	v.SetDefault("server.port", "8080")
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("database.driver", "sqlite3")
	v.SetDefault("database.source", "file:spotter.db?cache=shared&_fk=1")

	// Set defaults for keys to ensure Viper picks up environment variables
	v.SetDefault("navidrome.base_url", "")
	v.SetDefault("spotify.client_id", "")
	v.SetDefault("spotify.client_secret", "")
	v.SetDefault("spotify.redirect_url", "http://localhost:8080/auth/spotify/callback")
	v.SetDefault("lastfm.api_key", "")
	v.SetDefault("lastfm.shared_secret", "")
	v.SetDefault("lastfm.redirect_url", "")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
