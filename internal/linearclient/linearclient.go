package linearclient

import (
	"log/slog"

	"github.com/onelastcommit/noctra/internal/config"
	"github.com/onelastcommit/noctra/internal/linear"
	"github.com/onelastcommit/noctra/internal/state"
)

func New(cfg *config.Config, store *state.Store) *linear.Client {
	if cfg.OAuthPartiallyConfigured() {
		slog.Warn("linear actor=app config incomplete (need both client id and secret); using personal API key")
	}
	if cfg.ActorAppConfigured() {
		var ts linear.TokenStore
		if store != nil {
			ts = store
		}
		tm := linear.NewTokenManager(linear.TokenManagerConfig{
			ClientID:     cfg.LinearOAuthClientID,
			ClientSecret: cfg.LinearOAuthClientSecret,
			RefreshToken: cfg.LinearOAuthRefreshToken,
			Scope:        cfg.LinearOAuthScope,
			Store:        ts,
		})
		c := linear.New(cfg.LinearAPIKey)
		c.TokenFn = tm.Token
		c.OnAuthError = tm.ForceRefresh
		c.FallbackAPIKey = cfg.LinearAPIKey
		return c
	}
	if cfg.LinearOAuthToken != "" {
		c := linear.NewOAuth(cfg.LinearOAuthToken)
		c.FallbackAPIKey = cfg.LinearAPIKey
		return c
	}
	return linear.New(cfg.LinearAPIKey)
}
