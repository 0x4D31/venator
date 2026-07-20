package yamlshape_test

import (
	"strings"
	"testing"

	"github.com/0x4D31/venator/internal/config"
	"github.com/0x4D31/venator/internal/yamlshape"
	"go.yaml.in/yaml/v3"
)

type exampleShape struct {
	Name    string            `yaml:"name"`
	Enabled bool              `yaml:"enabled"`
	Labels  []string          `yaml:"labels"`
	Headers map[string]string `yaml:"headers"`
	Secret  *string           `yaml:"secret"`
	Timeout config.Duration   `yaml:"timeout"`
}

func TestValidateTypesAcceptsAliasesInValues(t *testing.T) {
	document := decodeDocument(t, `
name: &shared-name detector
enabled: true
labels: [*shared-name]
headers:
  X-Detector: *shared-name
secret: *shared-name
timeout: 5s
`)
	if err := yamlshape.RejectAliasMappingKeys(document); err != nil {
		t.Fatal(err)
	}
	if err := yamlshape.ValidateTypes(document, exampleShape{}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTypesRejectsImplicitCoercion(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		message string
	}{
		{name: "string", yaml: "name: 123\n", message: "name must be a YAML string"},
		{name: "boolean", yaml: "enabled: null\n", message: "enabled must be a YAML boolean"},
		{name: "sequence item", yaml: "labels: [valid, null]\n", message: "labels[1] must be a YAML string"},
		{name: "map value", yaml: "headers: {Authorization: null}\n", message: "Authorization must be a YAML string"},
		{name: "pointer", yaml: "secret: null\n", message: "secret must not be null"},
		{name: "custom scalar", yaml: "timeout: null\n", message: "timeout must not be null"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := yamlshape.ValidateTypes(decodeDocument(t, test.yaml), exampleShape{})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestRejectAliasMappingKeysWalksNestedMappings(t *testing.T) {
	document := decodeDocument(t, `
name: &key Authorization
headers:
  *key: token
`)
	if err := yamlshape.RejectAliasMappingKeys(document); err == nil || !strings.Contains(err.Error(), "aliases are not supported as mapping keys") {
		t.Fatalf("error = %v", err)
	}
}

func decodeDocument(t *testing.T, contents string) *yaml.Node {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(contents), &document); err != nil {
		t.Fatal(err)
	}
	return &document
}
