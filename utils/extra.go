package utils

import prism "github.com/nutanix-cloud-native/prism-go-client/v3"

// BuildReference creates a reference to the defined object.
func BuildReference(uuid, kind string) *prism.Reference {
	return &prism.Reference{
		Kind: new(kind),
		UUID: new(uuid),
		Name: nil,
	}
}
