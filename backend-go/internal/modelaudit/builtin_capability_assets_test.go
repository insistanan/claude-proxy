package modelaudit

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestBuiltinCapabilityAssetsHaveStableCatalog(t *testing.T) {
	assets, err := NewBuiltinCapabilityAssets()
	if err != nil {
		t.Fatal(err)
	}
	catalog := assets.Catalog()
	if !catalog.Available || catalog.Reason != "" || len(catalog.Packages) != 1 || len(catalog.Presets) != 3 {
		t.Fatalf("内置能力目录 = %#v", catalog)
	}
	packageSnapshot := catalog.Packages[0]
	expectedPackageRef := VersionedRef{ID: BuiltinCapabilityPackageID, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"}
	const expectedPackageSHA256 = "4cf818595e6533e9cc23f7482c00b44ddc9c4d7ebe0c9b1bfd0bbbb020a1e9fe"
	if packageSnapshot.Package.Ref != expectedPackageRef || packageSnapshot.SHA256 != expectedPackageSHA256 {
		t.Fatalf("内置能力任务包版本或哈希变化：ref=%#v sha256=%q", packageSnapshot.Package.Ref, packageSnapshot.SHA256)
	}
	expectedModes := []CapabilityPresetMode{CapabilityPresetQuick, CapabilityPresetStandard, CapabilityPresetDeep}
	expectedRequests := []int{7, 14, 21}
	for index, preset := range catalog.Presets {
		expectedRef := VersionedRef{
			ID:                    "capability.preset.builtin." + string(expectedModes[index]),
			SemanticVersion:       "1.0.0",
			ImplementationVersion: "builtin-1",
		}
		if preset.Ref != expectedRef || preset.Package != expectedPackageRef || preset.Mode != expectedModes[index] ||
			preset.Limits.Requests != expectedRequests[index] || len(preset.Tasks) != 7 {
			t.Fatalf("第 %d 个能力预设不稳定：%#v", index, preset)
		}
	}
	encoded, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"expectedOutput", "AUDIT-", `\"323\"`, "K12-7"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("能力目录泄露实例答案标记 %q：%s", forbidden, encoded)
		}
	}
}

func TestBuiltinCapabilityInstanceGenerationIsSeededAndReproducible(t *testing.T) {
	assets, err := NewBuiltinCapabilityAssets()
	if err != nil {
		t.Fatal(err)
	}
	target := TargetSnapshot{
		ChannelID: "channel-responses", ChannelKind: ChannelKindResponses, Protocol: ProtocolResponses,
		RequestedModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol", Thinking: ThinkingMedium,
		RequestProfile: "responses.standard.v1", CapturedAt: time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC),
	}
	changed := 0
	for _, task := range assets.packageSnapshot.Package.Tasks {
		first, err := assets.GenerateInstance("audit-run-seeded", target, task, 0, 1, target.CapturedAt)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := assets.GenerateInstance("audit-run-seeded", target, task, 0, 1, target.CapturedAt)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, repeated) {
			t.Fatalf("相同种子生成结果不一致：dimension=%s first=%#v repeated=%#v", task.Dimension, first, repeated)
		}
		alternative, err := assets.GenerateInstance("audit-run-seeded", target, task, 0, 99_999, target.CapturedAt)
		if err != nil {
			t.Fatal(err)
		}
		if first.InputSHA256 != alternative.InputSHA256 || !bytes.Equal(first.ExpectedOutput, alternative.ExpectedOutput) {
			changed++
		}
	}
	if changed != 5 {
		t.Fatalf("不同种子应改变五个参数化维度，实际改变 %d 个", changed)
	}
}
