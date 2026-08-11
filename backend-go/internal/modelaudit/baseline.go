package modelaudit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const BaselineSchemaVersion = "1"

type BaselineOrigin string

const (
	BaselineOriginLocalExternal BaselineOrigin = "local_external"
	BaselineOriginBuiltin       BaselineOrigin = "builtin"
	BaselineOriginUserGenerated BaselineOrigin = "user_generated"
)

func (o BaselineOrigin) Valid() bool {
	return o == BaselineOriginLocalExternal || o == BaselineOriginBuiltin || o == BaselineOriginUserGenerated
}

type BaselineRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func (r BaselineRef) Validate() error {
	if !stableIDPattern.MatchString(r.ID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("基线 ID %q 无效", r.ID))
	}
	if !semanticVersionPattern.MatchString(r.Version) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("基线版本 %q 无效", r.Version))
	}
	if !validSHA256(r.SHA256) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线 SHA-256 无效")
	}
	return nil
}

type FeatureSchemaRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type BaselineAuthorization struct {
	License         string `json:"license,omitempty"`
	Authorization   string `json:"authorization,omitempty"`
	Redistributable bool   `json:"redistributable"`
}

type BaselineManifest struct {
	SchemaVersion        string                `json:"schemaVersion"`
	Ref                  BaselineRef           `json:"ref"`
	Origin               BaselineOrigin        `json:"origin"`
	TargetModels         []string              `json:"targetModels"`
	Protocols            []Protocol            `json:"protocols"`
	ThinkingLevels       []ThinkingLevel       `json:"thinkingLevels,omitempty"`
	CollectionStartedAt  time.Time             `json:"collectionStartedAt"`
	CollectionEndedAt    time.Time             `json:"collectionEndedAt"`
	SampleSize           int                   `json:"sampleSize"`
	FeatureSchema        FeatureSchemaRef      `json:"featureSchema"`
	Source               string                `json:"source"`
	GenerationMethod     string                `json:"generationMethod"`
	Authorization        BaselineAuthorization `json:"authorization"`
	CompatibleStrategies []VersionedRef        `json:"compatibleStrategies"`
	LocalSourcePath      string                `json:"localSourcePath,omitempty"`
}

type BaselineEnvelope struct {
	Manifest BaselineManifest `json:"manifest"`
	Data     json.RawMessage  `json:"data"`
}

type BaselineAsset struct {
	Manifest BaselineManifest `json:"manifest"`
	Data     json.RawMessage  `json:"data"`
}

type BaselineSnapshot struct {
	SchemaVersion        string                `json:"schemaVersion"`
	Ref                  BaselineRef           `json:"ref"`
	Origin               BaselineOrigin        `json:"origin"`
	TargetModels         []string              `json:"targetModels"`
	Protocols            []Protocol            `json:"protocols"`
	ThinkingLevels       []ThinkingLevel       `json:"thinkingLevels,omitempty"`
	CollectionStartedAt  time.Time             `json:"collectionStartedAt"`
	CollectionEndedAt    time.Time             `json:"collectionEndedAt"`
	SampleSize           int                   `json:"sampleSize"`
	FeatureSchema        FeatureSchemaRef      `json:"featureSchema"`
	Source               string                `json:"source"`
	GenerationMethod     string                `json:"generationMethod"`
	Authorization        BaselineAuthorization `json:"authorization"`
	CompatibleStrategies []VersionedRef        `json:"compatibleStrategies"`
	LocalSourcePath      string                `json:"localSourcePath,omitempty"`
}

func (s BaselineSnapshot) Validate() error {
	return validateBaselineManifest(BaselineManifest{
		SchemaVersion: s.SchemaVersion, Ref: s.Ref, Origin: s.Origin,
		TargetModels: s.TargetModels, Protocols: s.Protocols, ThinkingLevels: s.ThinkingLevels,
		CollectionStartedAt: s.CollectionStartedAt, CollectionEndedAt: s.CollectionEndedAt,
		SampleSize: s.SampleSize, FeatureSchema: s.FeatureSchema, Source: s.Source,
		GenerationMethod: s.GenerationMethod, Authorization: s.Authorization,
		CompatibleStrategies: s.CompatibleStrategies, LocalSourcePath: s.LocalSourcePath,
	})
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func ImportBaseline(localSourcePath string, raw []byte) (BaselineAsset, error) {
	if len(raw) == 0 {
		return BaselineAsset{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线文件不能为空")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope BaselineEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return BaselineAsset{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线 envelope JSON 无效", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("只能包含一个 JSON 对象")
		}
		return BaselineAsset{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线 envelope JSON 无效", err)
	}
	canonicalData, digest, err := canonicalJSONObject(envelope.Data)
	if err != nil {
		return BaselineAsset{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线 data 无效", err)
	}
	envelope.Manifest.LocalSourcePath = strings.TrimSpace(localSourcePath)
	if envelope.Manifest.Ref.SHA256 != digest {
		return BaselineAsset{}, contractError(
			ErrorCodeInvalidRequest, ErrorCategoryRequest,
			fmt.Sprintf("基线内容哈希不匹配：声明 %q，实际 %q", envelope.Manifest.Ref.SHA256, digest),
		)
	}
	if err := validateBaselineManifest(envelope.Manifest); err != nil {
		return BaselineAsset{}, err
	}
	return BaselineAsset{Manifest: cloneBaselineManifest(envelope.Manifest), Data: canonicalData}, nil
}

func NewBaselineSnapshot(asset BaselineAsset) (BaselineSnapshot, error) {
	if err := validateBaselineManifest(asset.Manifest); err != nil {
		return BaselineSnapshot{}, err
	}
	_, digest, err := canonicalJSONObject(asset.Data)
	if err != nil {
		return BaselineSnapshot{}, err
	}
	if digest != asset.Manifest.Ref.SHA256 {
		return BaselineSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线快照内容哈希已变化")
	}
	manifest := cloneBaselineManifest(asset.Manifest)
	return BaselineSnapshot{
		SchemaVersion: manifest.SchemaVersion, Ref: manifest.Ref, Origin: manifest.Origin, TargetModels: manifest.TargetModels,
		Protocols: manifest.Protocols, ThinkingLevels: manifest.ThinkingLevels,
		CollectionStartedAt: manifest.CollectionStartedAt, CollectionEndedAt: manifest.CollectionEndedAt, SampleSize: manifest.SampleSize,
		FeatureSchema: manifest.FeatureSchema, Source: manifest.Source, GenerationMethod: manifest.GenerationMethod,
		Authorization: manifest.Authorization, CompatibleStrategies: manifest.CompatibleStrategies, LocalSourcePath: manifest.LocalSourcePath,
	}, nil
}

func validateBaselineManifest(manifest BaselineManifest) error {
	if manifest.SchemaVersion != BaselineSchemaVersion {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("不支持基线 Schema 版本 %q", manifest.SchemaVersion))
	}
	if err := manifest.Ref.Validate(); err != nil {
		return err
	}
	if !manifest.Origin.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("基线来源类型 %q 无效", manifest.Origin))
	}
	if len(manifest.TargetModels) == 0 || len(manifest.Protocols) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线必须声明目标模型和适用协议")
	}
	for _, model := range manifest.TargetModels {
		if strings.TrimSpace(model) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线目标模型不能为空")
		}
	}
	for _, protocol := range manifest.Protocols {
		if !protocol.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("基线包含无效协议 %q", protocol))
		}
	}
	for _, thinking := range manifest.ThinkingLevels {
		if !thinking.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("基线包含无效思考档位 %q", thinking))
		}
	}
	if manifest.CollectionStartedAt.IsZero() || manifest.CollectionEndedAt.IsZero() || manifest.CollectionEndedAt.Before(manifest.CollectionStartedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线采集窗口无效")
	}
	if manifest.SampleSize <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线样本量必须大于 0")
	}
	if !stableIDPattern.MatchString(manifest.FeatureSchema.ID) || !semanticVersionPattern.MatchString(manifest.FeatureSchema.Version) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线特征 Schema 引用无效")
	}
	if strings.TrimSpace(manifest.Source) == "" || strings.TrimSpace(manifest.GenerationMethod) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线缺少来源或生成方法")
	}
	if len(manifest.CompatibleStrategies) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "基线必须声明兼容策略版本")
	}
	for _, strategy := range manifest.CompatibleStrategies {
		if err := strategy.Validate("兼容策略"); err != nil {
			return err
		}
	}
	if manifest.Origin == BaselineOriginLocalExternal && strings.TrimSpace(manifest.LocalSourcePath) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "本地外部基线缺少来源路径")
	}
	if manifest.Origin == BaselineOriginBuiltin {
		authorizationPresent := strings.TrimSpace(manifest.Authorization.License) != "" || strings.TrimSpace(manifest.Authorization.Authorization) != ""
		if !manifest.Authorization.Redistributable || !authorizationPresent {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "内置基线必须提供可再分发授权")
		}
	}
	return nil
}

func cloneBaselineManifest(manifest BaselineManifest) BaselineManifest {
	manifest.TargetModels = append([]string(nil), manifest.TargetModels...)
	manifest.Protocols = append([]Protocol(nil), manifest.Protocols...)
	manifest.ThinkingLevels = append([]ThinkingLevel(nil), manifest.ThinkingLevels...)
	manifest.CompatibleStrategies = append([]VersionedRef(nil), manifest.CompatibleStrategies...)
	return manifest
}
