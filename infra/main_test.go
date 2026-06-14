package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

var assetHashPattern = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

func TestThailandGiftshopStackTemplateSnapshot(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	assertJSONSnapshot(t, "thailandgiftshop-stack.template.json", normalizeTemplate(t, template.ToJSON()))
}

func TestLambdaBuildCommandSnapshot(t *testing.T) {
	assertSnapshot(t, "lambda-build-commands.txt", []byte(strings.Join([]string{
		"ssr: " + ssrLambdaBuildCommand,
		"admin: " + adminLambdaBuildCommand,
	}, "\n")))
}

func TestSSRProvisionedConcurrencyUsesDestroyableAliasVersion(t *testing.T) {
	defer jsii.Close()

	template := synthesizedNormalizedTemplate(t)
	parameters := requireMap(t, template["Parameters"], "template parameters")
	if _, ok := parameters[adminOriginHeaderVersionParameterName]; !ok {
		t.Fatalf("template is missing %s parameter", adminOriginHeaderVersionParameterName)
	}

	aliases := resourcesOfType(t, template, "AWS::Lambda::Alias")
	if len(aliases) != 1 {
		t.Fatalf("Lambda alias count = %d, want 1", len(aliases))
	}
	_, alias := onlyResource(t, aliases)
	aliasProps := resourceProperties(t, alias)
	if got := aliasProps["Name"]; got != ssrLambdaAliasName {
		t.Fatalf("SSR alias name = %v, want %q", got, ssrLambdaAliasName)
	}
	provisionedConfig := requireMap(t, aliasProps["ProvisionedConcurrencyConfig"], "alias provisioned concurrency config")
	if got := provisionedConfig["ProvisionedConcurrentExecutions"]; got != float64(ssrProvisionedConcurrency) {
		t.Fatalf("SSR provisioned concurrency = %v, want %d", got, ssrProvisionedConcurrency)
	}

	versions := resourcesOfType(t, template, "AWS::Lambda::Version")
	if len(versions) != 1 {
		t.Fatalf("Lambda version count = %d, want 1", len(versions))
	}
	versionID, version := onlyResource(t, versions)
	assertResourceNotRetained(t, versionID, version)
	versionProps := resourceProperties(t, version)
	if _, ok := versionProps["ProvisionedConcurrencyConfig"]; ok {
		t.Fatalf("%s configures provisioned concurrency; expected concurrency on alias only", versionID)
	}
	if !containsStringFragment(versionProps["Description"], adminOriginHeaderVersionParameterName) {
		t.Fatalf("%s Description does not reference %s; parameter-only origin header rotations would not publish a new version", versionID, adminOriginHeaderVersionParameterName)
	}
}

func TestSSRHttpIntegrationInvokesProvisionedAlias(t *testing.T) {
	defer jsii.Close()

	template := synthesizedNormalizedTemplate(t)
	integrationID, integration := resourceByIDPrefix(t, resourcesOfType(t, template, "AWS::ApiGatewayV2::Integration"), "SsrDefaultRouteSsrLambdaIntegration")
	integrationURI := resourceProperties(t, integration)["IntegrationUri"]
	if !containsStringFragment(integrationURI, ":"+ssrLambdaAliasName) && !containsStringFragment(integrationURI, ssrLambdaAliasName) {
		t.Fatalf("%s IntegrationUri does not reference SSR alias %q: %#v", integrationID, ssrLambdaAliasName, integrationURI)
	}

	permissionID, permission := resourceByIDPrefix(t, resourcesOfType(t, template, "AWS::Lambda::Permission"), "SsrDefaultRouteSsrLambdaIntegrationPermission")
	functionName := resourceProperties(t, permission)["FunctionName"]
	if !containsStringFragment(functionName, ":"+ssrLambdaAliasName) && !containsStringFragment(functionName, ssrLambdaAliasName) {
		t.Fatalf("%s FunctionName does not reference SSR alias %q: %#v", permissionID, ssrLambdaAliasName, functionName)
	}
}

func TestConcreteNonProductionRegionPanicSnapshot(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	panicValue := capturePanic(func() {
		NewThailandGiftshopStack(app, "TestStack", &ThailandGiftshopStackProps{
			StackProps: awscdk.StackProps{
				Env: &awscdk.Environment{
					Region: jsii.String("us-east-2"),
				},
			},
		})
	})
	if panicValue == nil {
		t.Fatal("NewThailandGiftshopStack did not panic for concrete non-production region")
	}

	assertSnapshot(t, "concrete-non-production-region-panic.txt", []byte(fmt.Sprint(panicValue)))
}

func synthesizedNormalizedTemplate(t *testing.T) map[string]any {
	t.Helper()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)
	return normalizeTemplate(t, template.ToJSON())
}

func resourcesOfType(t *testing.T, template map[string]any, resourceType string) map[string]map[string]any {
	t.Helper()

	resources := requireMap(t, template["Resources"], "template resources")
	matches := map[string]map[string]any{}
	for id, resource := range resources {
		resourceMap := requireMap(t, resource, "resource "+id)
		if resourceMap["Type"] == resourceType {
			matches[id] = resourceMap
		}
	}
	return matches
}

func onlyResource(t *testing.T, resources map[string]map[string]any) (string, map[string]any) {
	t.Helper()

	if len(resources) != 1 {
		t.Fatalf("resource count = %d, want 1", len(resources))
	}
	for id, resource := range resources {
		return id, resource
	}
	panic("unreachable")
}

func resourceByIDPrefix(t *testing.T, resources map[string]map[string]any, prefix string) (string, map[string]any) {
	t.Helper()

	matches := map[string]map[string]any{}
	for id, resource := range resources {
		if strings.HasPrefix(id, prefix) {
			matches[id] = resource
		}
	}
	return onlyResource(t, matches)
}

func resourceProperties(t *testing.T, resource map[string]any) map[string]any {
	t.Helper()
	return requireMap(t, resource["Properties"], "resource properties")
}

func requireMap(t *testing.T, value any, name string) map[string]any {
	t.Helper()

	valueMap, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s has type %T, want map[string]any", name, value)
	}
	return valueMap
}

func assertResourceNotRetained(t *testing.T, id string, resource map[string]any) {
	t.Helper()

	for _, policyName := range []string{"DeletionPolicy", "UpdateReplacePolicy"} {
		if got := resource[policyName]; got == "Retain" {
			t.Fatalf("%s has %s=Retain; Lambda versions must be destroyable to avoid version bloat", id, policyName)
		}
	}
}

func containsStringFragment(value any, fragment string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, fragment)
	case []any:
		for _, item := range typed {
			if containsStringFragment(item, fragment) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsStringFragment(item, fragment) {
				return true
			}
		}
	}
	return false
}

func capturePanic(fn func()) (panicValue any) {
	defer func() {
		panicValue = recover()
	}()
	fn()
	return nil
}

func assertJSONSnapshot(t *testing.T, name string, value any) {
	t.Helper()

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		t.Fatalf("marshal snapshot JSON: %v", err)
	}
	assertSnapshot(t, name, encoded.Bytes())
}

func assertSnapshot(t *testing.T, name string, got []byte) {
	t.Helper()

	path := filepath.Join("testdata", name)
	got = append(bytes.TrimSpace(got), '\n')
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create snapshot directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write snapshot %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("snapshot %s does not exist; run UPDATE_SNAPSHOTS=1 go test ./infra", path)
		}
		t.Fatalf("read snapshot %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("snapshot %s does not match; run UPDATE_SNAPSHOTS=1 go test ./infra to accept the new output\n%s", path, firstSnapshotDiff(want, got))
	}
}

func firstSnapshotDiff(want []byte, got []byte) string {
	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(string(got), "\n")
	lineCount := max(len(wantLines), len(gotLines))
	for line := range lineCount {
		var wantLine string
		if line < len(wantLines) {
			wantLine = wantLines[line]
		}
		var gotLine string
		if line < len(gotLines) {
			gotLine = gotLines[line]
		}
		if wantLine != gotLine {
			return fmt.Sprintf("first difference at line %d:\nwant: %s\ngot:  %s", line+1, wantLine, gotLine)
		}
	}
	return "snapshot lengths differ"
}

func normalizeTemplate(t *testing.T, templateJSON *map[string]any) map[string]any {
	t.Helper()

	encoded, err := json.Marshal(templateJSON)
	if err != nil {
		t.Fatalf("marshal synthesized template: %v", err)
	}

	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatalf("unmarshal synthesized template copy: %v", err)
	}
	stripCDKMetadata(normalized)
	redactVolatileTemplateValues(normalized)
	return normalized
}

func stripCDKMetadata(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if resources, ok := typed["Resources"].(map[string]any); ok {
			for id, resource := range resources {
				resourceMap, ok := resource.(map[string]any)
				if ok && resourceMap["Type"] == "AWS::CDK::Metadata" {
					delete(resources, id)
				}
			}
		}
		delete(typed, "Metadata")
		for _, item := range typed {
			stripCDKMetadata(item)
		}
	case []any:
		for _, item := range typed {
			stripCDKMetadata(item)
		}
	}
}

func redactVolatileTemplateValues(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			typed[key] = redactVolatileTemplateValues(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = redactVolatileTemplateValues(item)
		}
		return typed
	case string:
		return assetHashPattern.ReplaceAllString(typed, "<asset-hash>")
	default:
		return typed
	}
}
