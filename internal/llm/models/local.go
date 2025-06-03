package models

import (
	"cmp"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/spf13/viper"
)

const (
	ProviderLocal ModelProvider = "local"

	localModelsPath        = "v1/models"
	lmStudioBetaModelsPath = "api/v0/models"
)

func init() {
	cfg := config.Get()
	var endpoint string

	if cfg != nil && cfg.Providers != nil {
		if providerCfg, ok := cfg.Providers[ProviderLocal]; ok && providerCfg.LocalEndpoint != "" {
			endpoint = providerCfg.LocalEndpoint
			logging.Debug("Using local endpoint from config", "endpoint", endpoint)
		}
	}

	if endpoint == "" {
		envEndpoint := os.Getenv("LOCAL_ENDPOINT")
		if envEndpoint != "" {
			endpoint = envEndpoint
			logging.Debug("Using local endpoint from environment variable", "endpoint", endpoint)
		}
	}

	if endpoint == "" {
		logging.Debug("No local endpoint configured (checked config 'providers.local.localEndpoint' and LOCAL_ENDPOINT env var). Skipping local model loading.")
		return
	}

	localEndpointURL, err := url.Parse(endpoint)
	if err != nil {
		logging.Warn("Failed to parse local endpoint URL",
			"error", err,
			"endpointURL", endpoint,
		)
		return
	}

	load := func(url *url.URL, path string) []localModel {
		// Create a copy of the URL to avoid modifying the original localEndpointURL
		targetURL := *url
		targetURL.Path = path
		return listLocalModels(targetURL.String())
	}

	models := load(localEndpointURL, lmStudioBetaModelsPath)

	if len(models) == 0 {
		models = load(localEndpointURL, localModelsPath)
	}

	if len(models) == 0 {
		logging.Warn("No local models found",
			"endpointURL", localEndpointURL.String(),
		)
		return
	}

	loadLocalModels(models)

	// Set a dummy API key for the local provider so it's considered active
	// by the validation logic in config.go, even if no real API key is needed.
	viper.SetDefault("providers.local.apiKey", "dummy")
	ProviderPopularity[ProviderLocal] = 0 // Initialize popularity
}

type localModelList struct {
	Data []localModel `json:"data"`
}

type localModel struct {
	ID                  string `json:"id"`
	Object              string `json:"object"`
	Type                string `json:"type"`
	Publisher           string `json:"publisher"`
	Arch                string `json:"arch"`
	CompatibilityType   string `json:"compatibility_type"`
	Quantization        string `json:"quantization"`
	State               string `json:"state"`
	MaxContextLength    int64  `json:"max_context_length"`
	LoadedContextLength int64  `json:"loaded_context_length"`
}

func listLocalModels(modelsEndpoint string) []localModel {
	res, err := http.Get(modelsEndpoint)
	if err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		logging.Debug("Failed to list local models",
			"status", res.StatusCode,
			"endpoint", modelsEndpoint,
		)
	}

	var modelList localModelList
	if err = json.NewDecoder(res.Body).Decode(&modelList); err != nil {
		logging.Debug("Failed to list local models",
			"error", err,
			"endpoint", modelsEndpoint,
		)
	}

	var supportedModels []localModel
	for _, model := range modelList.Data {
		if strings.HasSuffix(modelsEndpoint, lmStudioBetaModelsPath) {
			if model.Object != "model" || model.Type != "llm" {
				logging.Debug("Skipping unsupported LMStudio model",
					"endpoint", modelsEndpoint,
					"id", model.ID,
					"object", model.Object,
					"type", model.Type,
				)

				continue
			}
		}

		supportedModels = append(supportedModels, model)
	}

	return supportedModels
}

func loadLocalModels(models []localModel) {
	for i, m := range models {
		model := convertLocalModel(m)
		SupportedModels[model.ID] = model

		if i == 0 || m.State == "loaded" {
			viper.SetDefault("agents.coder.model", model.ID)
			viper.SetDefault("agents.summarizer.model", model.ID)
			viper.SetDefault("agents.task.model", model.ID)
			viper.SetDefault("agents.title.model", model.ID)
		}
	}
}

func convertLocalModel(model localModel) Model {
	return Model{
		ID:                  ModelID("local." + model.ID),
		Name:                friendlyModelName(model.ID),
		Provider:            ProviderLocal,
		APIModel:            model.ID,
		ContextWindow:       cmp.Or(model.LoadedContextLength, 4096),
		DefaultMaxTokens:    cmp.Or(model.LoadedContextLength, 4096),
		CanReason:           true,
		SupportsAttachments: true,
	}
}

var modelInfoRegex = regexp.MustCompile(`(?i)^([a-z0-9]+)(?:[-_]?([rv]?\d[\.\d]*))?(?:[-_]?([a-z]+))?.*`)

func friendlyModelName(modelID string) string {
	mainID := modelID
	tag := ""

	if slash := strings.LastIndex(mainID, "/"); slash != -1 {
		mainID = mainID[slash+1:]
	}

	if at := strings.Index(modelID, "@"); at != -1 {
		mainID = modelID[:at]
		tag = modelID[at+1:]
	}

	match := modelInfoRegex.FindStringSubmatch(mainID)
	if match == nil {
		return modelID
	}

	capitalize := func(s string) string {
		if s == "" {
			return ""
		}
		runes := []rune(s)
		runes[0] = unicode.ToUpper(runes[0])
		return string(runes)
	}

	family := capitalize(match[1])
	version := ""
	label := ""

	if len(match) > 2 && match[2] != "" {
		version = strings.ToUpper(match[2])
	}

	if len(match) > 3 && match[3] != "" {
		label = capitalize(match[3])
	}

	var parts []string
	if family != "" {
		parts = append(parts, family)
	}
	if version != "" {
		parts = append(parts, version)
	}
	if label != "" {
		parts = append(parts, label)
	}
	if tag != "" {
		parts = append(parts, tag)
	}

	return strings.Join(parts, " ")
}
