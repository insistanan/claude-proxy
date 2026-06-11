// Package modelcatalog builds the externally visible model list and resolves
// Chat route aliases back to a pinned channel/key/upstream model.
package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/utils"
)

const (
	cacheTTL             = 20 * time.Minute
	modelsRequestTimeout = 30 * time.Second
	modelCreatedFallback = int64(0)
)

var (
	versionSuffixPattern = regexp.MustCompile(`/v\d+[a-z]*$`)
	unsafeModelIDChars   = regexp.MustCompile(`[^A-Za-z0-9._:-]+`)
	globalCache          = &catalogCache{}
)

type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

type ChatRoute struct {
	Alias         string
	UpstreamModel string
	ChannelIndex  int
	ChannelName   string
	BaseURL       string
	APIKey        string
	KeyID         string
	ServiceType   string
}

type catalogSnapshot struct {
	expiresAt time.Time
	models    []ModelEntry
	routes    map[string]ChatRoute
}

type catalogCache struct {
	mu         sync.RWMutex
	snapshot   catalogSnapshot
	refreshing bool
}

func OpenAIModels(ctx context.Context, cfgManager *config.ConfigManager) ModelsResponse {
	staticModels := staticFamilyModels()
	chatModels, _, err := chatCatalog(ctx, cfgManager, false)
	if err != nil {
		log.Printf("[Models-Chat] 获取 Chat 模型列表失败，使用静态模型兜底: %v", err)
	}

	data := make([]ModelEntry, 0, len(staticModels)+len(chatModels))
	data = append(data, staticModels...)
	data = append(data, chatModels...)
	return ModelsResponse{Object: "list", Data: data}
}

func ModelDetail(ctx context.Context, cfgManager *config.ConfigManager, modelID string) (ModelEntry, bool) {
	for _, model := range staticFamilyModels() {
		if model.ID == modelID {
			return model, true
		}
	}
	if !LooksLikeChatRouteAlias(modelID) {
		return ModelEntry{}, false
	}
	chatModels, _, err := chatCatalog(ctx, cfgManager, false)
	if err != nil {
		return ModelEntry{}, false
	}
	for _, model := range chatModels {
		if model.ID == modelID {
			return model, true
		}
	}
	return ModelEntry{}, false
}

func ResolveChatRoute(ctx context.Context, cfgManager *config.ConfigManager, modelID string) (ChatRoute, bool) {
	if !LooksLikeChatRouteAlias(modelID) {
		return ChatRoute{}, false
	}

	_, routes, err := chatCatalog(ctx, cfgManager, false)
	if err == nil {
		if route, ok := routes[modelID]; ok {
			return route, true
		}
	}

	_, routes, err = chatCatalog(ctx, cfgManager, true)
	if err != nil {
		return ChatRoute{}, false
	}
	route, ok := routes[modelID]
	return route, ok
}

func LooksLikeChatRouteAlias(modelID string) bool {
	return strings.Contains(modelID, "__c") && strings.Contains(modelID, "__k")
}

func staticFamilyModels() []ModelEntry {
	ids := []string{
		"opus",
		"sonnet",
		"haiku",
		"fable",
		"gpt",
		"codex",
		"gemini",
	}
	models := make([]ModelEntry, 0, len(ids))
	for _, id := range ids {
		models = append(models, ModelEntry{
			ID:      id,
			Object:  "model",
			Created: modelCreatedFallback,
			OwnedBy: "claude-proxy",
		})
	}
	return models
}

func chatCatalog(ctx context.Context, cfgManager *config.ConfigManager, forceRefresh bool) ([]ModelEntry, map[string]ChatRoute, error) {
	now := time.Now()
	if !forceRefresh {
		globalCache.mu.RLock()
		if now.Before(globalCache.snapshot.expiresAt) {
			models := cloneModels(globalCache.snapshot.models)
			routes := cloneRoutes(globalCache.snapshot.routes)
			globalCache.mu.RUnlock()
			return models, routes, nil
		}
		if len(globalCache.snapshot.models) > 0 {
			models := cloneModels(globalCache.snapshot.models)
			routes := cloneRoutes(globalCache.snapshot.routes)
			globalCache.mu.RUnlock()
			startBackgroundRefresh(cfgManager)
			return models, routes, nil
		}
		globalCache.mu.RUnlock()
	}

	return refreshChatCatalog(ctx, cfgManager)
}

func refreshChatCatalog(ctx context.Context, cfgManager *config.ConfigManager) ([]ModelEntry, map[string]ChatRoute, error) {
	models, routes, err := discoverChatModels(ctx, cfgManager)
	globalCache.mu.Lock()
	defer globalCache.mu.Unlock()
	if err != nil && len(globalCache.snapshot.models) > 0 {
		return cloneModels(globalCache.snapshot.models), cloneRoutes(globalCache.snapshot.routes), err
	}
	globalCache.snapshot = catalogSnapshot{
		expiresAt: time.Now().Add(cacheTTL),
		models:    cloneModels(models),
		routes:    cloneRoutes(routes),
	}
	return models, routes, err
}

func startBackgroundRefresh(cfgManager *config.ConfigManager) {
	globalCache.mu.Lock()
	if globalCache.refreshing {
		globalCache.mu.Unlock()
		return
	}
	globalCache.refreshing = true
	globalCache.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), modelsRequestTimeout)
		defer cancel()

		models, routes, err := discoverChatModels(ctx, cfgManager)

		globalCache.mu.Lock()
		defer globalCache.mu.Unlock()
		defer func() { globalCache.refreshing = false }()
		if err != nil {
			log.Printf("[Models-Chat] 后台刷新失败，继续使用旧缓存: %v", err)
			return
		}
		globalCache.snapshot = catalogSnapshot{
			expiresAt: time.Now().Add(cacheTTL),
			models:    cloneModels(models),
			routes:    cloneRoutes(routes),
		}
		log.Printf("[Models-Chat] 后台刷新完成: models=%d", len(models))
	}()
}

func discoverChatModels(ctx context.Context, cfgManager *config.ConfigManager) ([]ModelEntry, map[string]ChatRoute, error) {
	cfg := cfgManager.GetConfig()
	routes := make(map[string]ChatRoute)
	models := make([]ModelEntry, 0)
	usedAliases := make(map[string]bool)
	usedRouteTargets := make(map[string]bool)
	var errors []string

	for channelIndex, upstream := range cfg.ChatUpstream {
		if config.GetChannelStatus(&upstream) != config.ChannelStatusActive || !config.IsChannelSchedulable(&upstream) {
			continue
		}
		if len(upstream.APIKeys) == 0 {
			continue
		}

		baseURLs := upstream.GetAllBaseURLs()
		if len(baseURLs) == 0 {
			continue
		}

		for _, apiKey := range upstream.APIKeys {
			keyID := keyFingerprint(apiKey)
			for _, configured := range configuredChatModels(&upstream) {
				appendChatRoute(
					&models, routes, usedAliases, usedRouteTargets,
					configured.ClientModel, configured.UpstreamModel,
					channelIndex, &upstream, apiKey, keyID, baseURLs[0], "local",
				)
			}

			foundModels, baseURL, method, err := discoverModelsForKey(ctx, &upstream, apiKey, baseURLs)
			if err != nil {
				errors = append(errors, fmt.Sprintf("[%d]%s/%s: %v", channelIndex, upstream.Name, keyID, err))
				continue
			}

			for _, upstreamModel := range foundModels {
				appendChatRoute(
					&models, routes, usedAliases, usedRouteTargets,
					upstreamModel, upstreamModel,
					channelIndex, &upstream, apiKey, keyID, baseURL, method,
				)
			}
		}
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	if len(models) == 0 && len(errors) > 0 {
		return models, routes, fmt.Errorf("%s", strings.Join(errors, "; "))
	}
	return models, routes, nil
}

type configuredModel struct {
	ClientModel   string
	UpstreamModel string
}

func configuredChatModels(upstream *config.UpstreamConfig) []configuredModel {
	seen := make(map[string]bool)
	models := make([]configuredModel, 0, len(upstream.ModelMapping)+1)

	if model := strings.TrimSpace(upstream.DefaultModel); model != "" {
		models = append(models, configuredModel{ClientModel: model, UpstreamModel: model})
		seen[model] = true
	}

	for clientModel, upstreamModel := range upstream.ModelMapping {
		clientModel = strings.TrimSpace(clientModel)
		upstreamModel = strings.TrimSpace(upstreamModel)
		if clientModel == "" || upstreamModel == "" || seen[clientModel] {
			continue
		}
		models = append(models, configuredModel{ClientModel: clientModel, UpstreamModel: upstreamModel})
		seen[clientModel] = true
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ClientModel < models[j].ClientModel
	})
	return models
}

func appendChatRoute(
	models *[]ModelEntry,
	routes map[string]ChatRoute,
	usedAliases map[string]bool,
	usedRouteTargets map[string]bool,
	clientModel string,
	upstreamModel string,
	channelIndex int,
	upstream *config.UpstreamConfig,
	apiKey string,
	keyID string,
	baseURL string,
	method string,
) {
	targetKey := fmt.Sprintf("%d|%s|%s|%s", channelIndex, keyID, clientModel, upstreamModel)
	if usedRouteTargets[targetKey] {
		return
	}
	usedRouteTargets[targetKey] = true

	alias := uniqueAlias(clientModel, channelIndex, keyID, usedAliases)
	route := ChatRoute{
		Alias:         alias,
		UpstreamModel: upstreamModel,
		ChannelIndex:  channelIndex,
		ChannelName:   upstream.Name,
		BaseURL:       baseURL,
		APIKey:        apiKey,
		KeyID:         keyID,
		ServiceType:   strings.TrimSpace(upstream.ServiceType),
	}
	routes[alias] = route
	*models = append(*models, ModelEntry{
		ID:      alias,
		Object:  "model",
		Created: modelCreatedFallback,
		OwnedBy: fmt.Sprintf("chat:%s:%s", upstream.Name, method),
	})
}

func discoverModelsForKey(ctx context.Context, upstream *config.UpstreamConfig, apiKey string, baseURLs []string) ([]string, string, string, error) {
	methods := orderedDiscoveryMethods(upstream.ServiceType)
	var lastErr error

	for _, baseURL := range baseURLs {
		for _, method := range methods {
			models, err := fetchModelsByMethod(ctx, upstream, apiKey, baseURL, method)
			if err != nil {
				lastErr = err
				continue
			}
			models = dedupeStrings(models)
			if len(models) > 0 {
				log.Printf("[Models-Chat] 渠道 %s key=%s baseURL=%s method=%s 获取模型 %d 个",
					upstream.Name, keyFingerprint(apiKey), baseURL, method, len(models))
				return models, baseURL, method, nil
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("models endpoint returned no models")
	}
	return nil, "", "", lastErr
}

func orderedDiscoveryMethods(serviceType string) []string {
	all := []string{"openai-v1", "openai-root", "anthropic", "gemini", "ollama"}
	switch strings.ToLower(strings.TrimSpace(serviceType)) {
	case "claude", "anthropic":
		return []string{"anthropic", "openai-v1", "openai-root", "gemini", "ollama"}
	case "gemini":
		return []string{"gemini", "openai-v1", "openai-root", "anthropic", "ollama"}
	default:
		return all
	}
}

func fetchModelsByMethod(ctx context.Context, upstream *config.UpstreamConfig, apiKey, baseURL, method string) ([]string, error) {
	reqURL := ""
	switch method {
	case "openai-v1":
		reqURL = buildVersionedModelsURL(baseURL)
	case "openai-root":
		reqURL = buildRootModelsURL(baseURL)
	case "anthropic":
		reqURL = buildAnthropicModelsURL(baseURL)
	case "gemini":
		reqURL = buildGeminiModelsURL(baseURL, apiKey)
	case "ollama":
		reqURL = buildOllamaTagsURL(baseURL)
	default:
		return nil, fmt.Errorf("unknown discovery method: %s", method)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	applyModelsHeaders(req, apiKey, method)

	client := httpclient.GetManager().GetStandardClient(modelsRequestTimeout, upstream.InsecureSkipVerify)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	body = utils.DecompressGzipIfNeeded(resp, body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %d", method, resp.StatusCode)
	}

	switch method {
	case "openai-v1", "openai-root":
		return parseOpenAIModels(body)
	case "anthropic":
		return parseAnthropicModels(body)
	case "gemini":
		return parseGeminiModels(body)
	case "ollama":
		return parseOllamaModels(body)
	default:
		return nil, fmt.Errorf("unknown discovery method: %s", method)
	}
}

func applyModelsHeaders(req *http.Request, apiKey, method string) {
	req.Header.Set("Content-Type", "application/json")
	switch method {
	case "anthropic":
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		req.Header.Set("x-goog-api-key", apiKey)
	default:
		utils.SetAuthenticationHeader(req.Header, apiKey)
	}
}

func parseOpenAIModels(body []byte) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

func parseAnthropicModels(body []byte) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

func parseGeminiModels(body []byte) ([]string, error) {
	var resp struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	models := make([]string, 0, len(resp.Models))
	for _, item := range resp.Models {
		if !supportsGeminiGeneration(item.SupportedGenerationMethods) {
			continue
		}
		id := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

func parseOllamaModels(body []byte) ([]string, error) {
	var resp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(resp.Models))
	for _, item := range resp.Models {
		if id := strings.TrimSpace(item.Name); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

func supportsGeminiGeneration(methods []string) bool {
	if len(methods) == 0 {
		return true
	}
	for _, method := range methods {
		switch method {
		case "generateContent", "streamGenerateContent":
			return true
		}
	}
	return false
}

func buildVersionedModelsURL(baseURL string) string {
	baseURL, skipVersionPrefix := normalizeBaseURL(baseURL)
	endpoint := "/models"
	if !skipVersionPrefix && !versionSuffixPattern.MatchString(baseURL) {
		endpoint = "/v1" + endpoint
	}
	return baseURL + endpoint
}

func buildRootModelsURL(baseURL string) string {
	baseURL, _ = normalizeBaseURL(baseURL)
	return baseURL + "/models"
}

func buildAnthropicModelsURL(baseURL string) string {
	return buildVersionedModelsURL(baseURL)
}

func buildGeminiModelsURL(baseURL, apiKey string) string {
	baseURL, _ = normalizeBaseURL(baseURL)
	if strings.HasSuffix(baseURL, "/v1") || strings.HasSuffix(baseURL, "/v1beta") {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
		baseURL = strings.TrimSuffix(baseURL, "/v1beta")
	}
	u := baseURL + "/v1beta/models"
	if apiKey == "" {
		return u
	}
	return u + "?key=" + url.QueryEscape(apiKey)
}

func buildOllamaTagsURL(baseURL string) string {
	baseURL, _ = normalizeBaseURL(baseURL)
	return baseURL + "/api/tags"
}

func normalizeBaseURL(baseURL string) (string, bool) {
	baseURL = strings.TrimSpace(baseURL)
	skipVersionPrefix := strings.HasSuffix(baseURL, "#")
	if skipVersionPrefix {
		baseURL = strings.TrimSuffix(baseURL, "#")
	}
	return strings.TrimRight(baseURL, "/"), skipVersionPrefix
}

func uniqueAlias(model string, channelIndex int, keyID string, used map[string]bool) string {
	slug := sanitizeModelID(model)
	if slug == "" {
		slug = "model"
	}
	base := fmt.Sprintf("%s__c%d__k%s", slug, channelIndex, keyID)
	if !used[base] {
		used[base] = true
		return base
	}

	hash := shortHash(model, 6)
	alias := base + "__m" + hash
	for i := 2; used[alias]; i++ {
		alias = fmt.Sprintf("%s__m%s_%d", base, hash, i)
	}
	used[alias] = true
	return alias
}

func sanitizeModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "models/")
	model = unsafeModelIDChars.ReplaceAllString(model, "-")
	model = strings.Trim(model, "-")
	for strings.Contains(model, "--") {
		model = strings.ReplaceAll(model, "--", "-")
	}
	return model
}

func keyFingerprint(apiKey string) string {
	return shortHash(apiKey, 8)
}

func shortHash(value string, n int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	if n > len(encoded) {
		n = len(encoded)
	}
	return encoded[:n]
}

func dedupeStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func cloneModels(models []ModelEntry) []ModelEntry {
	if models == nil {
		return nil
	}
	cloned := make([]ModelEntry, len(models))
	copy(cloned, models)
	return cloned
}

func cloneRoutes(routes map[string]ChatRoute) map[string]ChatRoute {
	if routes == nil {
		return nil
	}
	cloned := make(map[string]ChatRoute, len(routes))
	for k, v := range routes {
		cloned[k] = v
	}
	return cloned
}
