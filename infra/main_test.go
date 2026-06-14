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

var (
	assetHashPattern              = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	lambdaVersionLogicalIDPattern = regexp.MustCompile(`\b(SsrLambdaCurrentVersion[0-9A-F]{8})[0-9a-f]{32}\b`)
)

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
			redactedKey := redactVolatileTemplateString(key)
			redactedValue := redactVolatileTemplateValues(item)
			if redactedKey != key {
				delete(typed, key)
			}
			typed[redactedKey] = redactedValue
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = redactVolatileTemplateValues(item)
		}
		return typed
	case string:
		return redactVolatileTemplateString(typed)
	default:
		return typed
	}
}

func redactVolatileTemplateString(value string) string {
	value = assetHashPattern.ReplaceAllString(value, "<asset-hash>")
	return lambdaVersionLogicalIDPattern.ReplaceAllString(value, "${1}<asset-hash>")
}
