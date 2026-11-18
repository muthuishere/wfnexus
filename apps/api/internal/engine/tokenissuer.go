package engine

import "github.com/muthuishere/wfnexus/apps/api/internal/config"

// TokenIssuerConfig is the external token issuer this server accepts, if any.
func (e *Engine) TokenIssuerConfig() config.TokenIssuer { return e.cfg.TokenIssuer }
