package domain

const (
	PrivacyExternalSearch = "local_inference_external_search"
	PrivacyLocalSearXNG   = "local_searxng"
	PrivacyOffline        = "offline"
)

// Empty identifies policies created before explicit privacy contracts existed.
func EffectivePrivacyMode(mode string) string {
	if mode == "" {
		return PrivacyExternalSearch
	}
	return mode
}
