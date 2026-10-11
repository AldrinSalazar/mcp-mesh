package config

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SetNodeName sets node.name, keeping comments. The hub binds a node key
// to a name, so a machine whose hostname differs from the key's name sets
// it here (init --name).
func SetNodeName(path, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("node name is empty")
	}
	err := editConfig(path, func(root *yaml.Node) error {
		if i := mappingIndex(root, "node"); i >= 0 {
			node := root.Content[i+1]
			if node.Kind != yaml.MappingNode {
				return errors.New("node must be a mapping")
			}
			if j := mappingIndex(node, "name"); j >= 0 {
				node.Content[j+1] = stringNode(name)
			} else {
				node.Content = append(node.Content, stringNode("name"), stringNode(name))
			}
			return nil
		}
		key := stringNode("node")
		value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{stringNode("name"), stringNode(name)}}
		// The starter config documents node as a commented-out block in the
		// head comment of the key after hub; the real entry replaces it.
		at := len(root.Content)
		if h := mappingIndex(root, "hub"); h >= 0 {
			at = h + 2
		}
		if at < len(root.Content) {
			next := root.Content[at]
			if strings.Contains(next.HeadComment, "# node:") {
				key.HeadComment = "# This machine's name in the mesh; the hub binds its node key to it."
				next.HeadComment = ""
			}
		}
		root.Content = append(root.Content[:at], append([]*yaml.Node{key, value}, root.Content[at:]...)...)
		return nil
	})
	if err != nil {
		return fmt.Errorf("set node.name in %s: %w", path, err)
	}
	return nil
}
