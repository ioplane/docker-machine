package driver

import (
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	yamlSequenceTag = "!!seq"
	yamlMappingTag  = "!!map"
)

func (d *NutanixDriver) cloudInitUserData(pubKey []byte) ([]byte, error) {
	if d.CloudInit == "" {
		data := "#cloud-config\r\nusers:\r\n - name: root\r\n   ssh_authorized_keys:\r\n    - " + string(pubKey)

		return []byte(data), nil
	}

	document, err := cloudInitDocument(d.CloudInit)
	if err != nil {
		return nil, err
	}

	fields, err := cloudInitFields(document.Content[0])
	if err != nil {
		return nil, err
	}

	users := fields["users"]
	if users == nil {
		users = &yaml.Node{Kind: yaml.SequenceNode, Tag: yamlSequenceTag}

		document.Content[0].Content = append(document.Content[0].Content, buildScalarNodes("users")...)
		document.Content[0].Content = append(document.Content[0].Content, users)
	}

	if users.Kind != yaml.SequenceNode || users.Tag != yamlSequenceTag {
		return nil, errCloudInitSyntax
	}

	root, err := cloudInitRoot(users)
	if err != nil {
		return nil, err
	}

	err = mergeRootKey(root, strings.TrimSpace(string(pubKey)))
	if err != nil {
		return nil, err
	}

	data, err := yaml.Marshal(document)
	if err != nil {
		return nil, errCloudInitSyntax
	}

	return data, nil
}

func cloudInitDocument(input string) (*yaml.Node, error) {
	input = strings.ReplaceAll(strings.ReplaceAll(input, "\\n", "\n"), "\\r", "\r")

	header, _, _ := strings.Cut(input, "\n")
	if strings.TrimSpace(header) != "#cloud-config" {
		return nil, errCloudInitSyntax
	}

	decoder := yaml.NewDecoder(strings.NewReader(input))

	var document yaml.Node

	err := decoder.Decode(&document)
	if errors.Is(err, io.EOF) {
		return &yaml.Node{Kind: yaml.DocumentNode, HeadComment: "#cloud-config", Content: []*yaml.Node{
			{Kind: yaml.MappingNode, Tag: yamlMappingTag},
		}}, nil
	}

	if err != nil || len(document.Content) != 1 {
		return nil, errCloudInitSyntax
	}

	var extra yaml.Node

	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		return nil, errCloudInitSyntax
	}

	return &document, nil
}

// Only direct string keys are editable; merge keys and aliases make ownership ambiguous.
func cloudInitFields(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode || node.Tag != yamlMappingTag {
		return nil, errCloudInitSyntax
	}

	fields := make(map[string]*yaml.Node, len(node.Content)/categoryPairLength)
	for index := 0; index < len(node.Content); index += categoryPairLength {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != yamlStringTag || fields[key.Value] != nil {
			return nil, errCloudInitSyntax
		}

		fields[key.Value] = node.Content[index+1]
	}

	return fields, nil
}

func cloudInitRoot(users *yaml.Node) (*yaml.Node, error) {
	var root *yaml.Node

	for _, user := range users.Content {
		name := user
		if user.Kind == yaml.MappingNode {
			fields, err := cloudInitFields(user)
			if err != nil {
				return nil, err
			}

			name = fields["name"]
		} else if user.Kind != yaml.ScalarNode {
			return nil, errCloudInitSyntax
		}

		if name == nil {
			continue // cloud-init also supports snapuser entries without name.
		}

		if name.Kind != yaml.ScalarNode || name.Tag != yamlStringTag || name.Value == "" {
			return nil, errCloudInitSyntax
		}

		if name.Value != "root" {
			continue
		}

		if root != nil {
			return nil, errCloudInitSyntax
		}

		root = user
	}

	if root == nil {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: yamlMappingTag, Content: buildStringNodes("name", "root", "")}
		users.Content = append(users.Content, root)
	}

	if root.Kind == yaml.ScalarNode {
		root.Kind, root.Tag, root.Value = yaml.MappingNode, yamlMappingTag, ""
		root.Content = buildStringNodes("name", "root", "")
	}

	return root, nil
}

func mergeRootKey(root *yaml.Node, key string) error {
	fields, err := cloudInitFields(root)
	if err != nil {
		return err
	}

	keys := fields["ssh_authorized_keys"]
	if keys == nil {
		keys = &yaml.Node{Kind: yaml.SequenceNode, Tag: yamlSequenceTag}

		root.Content = append(root.Content, buildScalarNodes("ssh_authorized_keys")...)
		root.Content = append(root.Content, keys)
	}

	if keys.Kind != yaml.SequenceNode || keys.Tag != yamlSequenceTag {
		return errCloudInitSyntax
	}

	present := false

	for _, existing := range keys.Content {
		if existing.Kind != yaml.ScalarNode || existing.Tag != yamlStringTag {
			return errCloudInitSyntax
		}

		present = present || existing.Value == key
	}

	if !present {
		keys.Content = append(keys.Content, buildScalarNodes(key)...)
	}

	if fields["sudo"] == nil {
		root.Content = append(root.Content, buildStringNodes("sudo", "ALL=(ALL) NOPASSWD:ALL", "")...)
	}

	return nil
}
