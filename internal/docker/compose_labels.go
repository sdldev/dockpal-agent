package docker

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetServiceLabel sets or removes a label on every service in a compose YAML.
// An empty value removes the label.
func SetServiceLabel(composeYAML, key, value string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("label key must not be empty")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(composeYAML), &root); err != nil {
		return "", fmt.Errorf("invalid compose YAML: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return "", fmt.Errorf("empty compose YAML")
	}
	rootMap := root.Content[0]
	if rootMap.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose root is not a mapping")
	}
	servicesNode := findYAMLMapValue(rootMap, "services")
	if servicesNode == nil {
		return "", fmt.Errorf("no services defined in compose file")
	}
	if servicesNode.Kind != yaml.MappingNode {
		return "", fmt.Errorf("services is not a mapping")
	}
	if len(servicesNode.Content) == 0 {
		return "", fmt.Errorf("no services defined in compose file")
	}

	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		svc := servicesNode.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if err := setServiceLabelOnNode(svc, key, value); err != nil {
			return "", err
		}
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return "", fmt.Errorf("failed to marshal compose YAML: %w", err)
	}
	return string(out), nil
}

func findYAMLMapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func removeYAMLMapEntry(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func newLabelStringNode(value string) *yaml.Node {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: value,
		Style: yaml.DoubleQuotedStyle,
	}
}

func setServiceLabelOnNode(svc *yaml.Node, key, value string) error {
	labelsVal := findYAMLMapValue(svc, "labels")
	if labelsVal == nil {
		if value == "" {
			return nil
		}
		svc.Content = append(svc.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "labels"},
			&yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
					newLabelStringNode(value),
				},
			},
		)
		return nil
	}

	switch labelsVal.Kind {
	case yaml.MappingNode:
		idx := -1
		for i := 0; i+1 < len(labelsVal.Content); i += 2 {
			if labelsVal.Content[i].Value == key {
				idx = i
				break
			}
		}
		if value == "" {
			if idx >= 0 {
				labelsVal.Content = append(labelsVal.Content[:idx], labelsVal.Content[idx+2:]...)
			}
			if len(labelsVal.Content) == 0 {
				removeYAMLMapEntry(svc, "labels")
			}
			return nil
		}
		if idx >= 0 {
			vn := labelsVal.Content[idx+1]
			vn.Kind = yaml.ScalarNode
			vn.Tag = "!!str"
			vn.Value = value
			vn.Style = yaml.DoubleQuotedStyle
			return nil
		}
		labelsVal.Content = append(labelsVal.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			newLabelStringNode(value),
		)
		return nil

	case yaml.SequenceNode:
		prefix := key + "="
		idx := -1
		for i, item := range labelsVal.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			if item.Value == key || strings.HasPrefix(item.Value, prefix) {
				idx = i
				break
			}
		}
		if value == "" {
			if idx >= 0 {
				labelsVal.Content = append(labelsVal.Content[:idx], labelsVal.Content[idx+1:]...)
			}
			if len(labelsVal.Content) == 0 {
				removeYAMLMapEntry(svc, "labels")
			}
			return nil
		}
		entry := key + "=" + value
		if idx >= 0 {
			labelsVal.Content[idx].Tag = "!!str"
			labelsVal.Content[idx].Value = entry
			return nil
		}
		labelsVal.Content = append(labelsVal.Content, &yaml.Node{
			Kind: yaml.ScalarNode, Tag: "!!str", Value: entry,
		})
		return nil

	case yaml.ScalarNode:
		if value == "" {
			return nil
		}
		labelsVal.Kind = yaml.MappingNode
		labelsVal.Tag = "!!map"
		labelsVal.Value = ""
		labelsVal.Style = 0
		labelsVal.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			newLabelStringNode(value),
		}
		return nil

	default:
		return fmt.Errorf("unsupported labels node kind %d", labelsVal.Kind)
	}
}
